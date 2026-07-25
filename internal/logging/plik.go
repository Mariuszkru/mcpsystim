// Package logging zapisuje logi serwera do pliku z rotacją po rozmiarze.
//
// Serwer domyślnie loguje na stdout i tam zbiera je Docker — to wystarcza, dopóki
// wystarcza `docker compose logs`. Plik przydaje się wtedy, gdy logi mają przeżyć
// przebudowanie kontenera albo trafić do czegoś, co czyta ze ścieżki.
//
// Rotacja jest tu, a nie w logrotate, bo obraz jest distroless: nie ma w nim
// powłoki ani crona, które mogłyby plik obrócić. Bez rotacji plik na wolumenie
// rósłby bez końca i po cichu zapchał dysk hosta.
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// PlikZRotacja to io.Writer zapisujący do pliku, który po przekroczeniu
// zadanego rozmiaru jest odkładany jako kopia, a zapis idzie do nowego pliku.
//
// Kopie mają przyrostki .1, .2, … — .1 jest najmłodsza. Bezpieczny do użycia
// z wielu goroutine, bo handler slog może pisać równolegle.
type PlikZRotacja struct {
	sciezka   string
	maxBajtow int64
	kopie     int

	mu      sync.Mutex
	plik    *os.File
	rozmiar int64
}

// NowyPlikZRotacja otwiera plik logu, tworząc katalog, jeśli go nie ma.
//
// maxMB to rozmiar, po którym plik jest rotowany. kopie to liczba trzymanych
// starych plików; zero oznacza, że po rotacji stary log jest kasowany.
func NowyPlikZRotacja(sciezka string, maxMB, kopie int) (*PlikZRotacja, error) {
	if sciezka == "" {
		return nil, fmt.Errorf("logging: ścieżka pliku logu jest wymagana")
	}
	if maxMB <= 0 {
		return nil, fmt.Errorf("logging: maksymalny rozmiar pliku logu musi być dodatni, dostałem %d MB", maxMB)
	}
	if kopie < 0 {
		return nil, fmt.Errorf("logging: liczba kopii nie może być ujemna, dostałem %d", kopie)
	}

	p := &PlikZRotacja{
		sciezka:   sciezka,
		maxBajtow: int64(maxMB) << 20,
		kopie:     kopie,
	}
	if err := p.otworz(); err != nil {
		return nil, err
	}
	return p, nil
}

// otworz otwiera plik do dopisywania i odczytuje jego bieżący rozmiar.
func (p *PlikZRotacja) otworz() error {
	if err := os.MkdirAll(filepath.Dir(p.sciezka), 0o750); err != nil {
		return fmt.Errorf("logging: nie udało się utworzyć katalogu na logi %s: %w. "+
			"Sprawdź, czy wolumen jest zamontowany i zapisywalny dla użytkownika kontenera (UID 65532)",
			filepath.Dir(p.sciezka), err)
	}
	f, err := os.OpenFile(p.sciezka, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("logging: nie udało się otworzyć pliku logu %s: %w", p.sciezka, err)
	}
	// Po restarcie dopisujemy do istniejącego pliku, więc rozmiar liczymy od tego,
	// co już w nim jest — inaczej rotacja spóźniłaby się o całą poprzednią zawartość.
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("logging: nie udało się odczytać rozmiaru pliku logu %s: %w", p.sciezka, err)
	}

	p.plik = f
	p.rozmiar = info.Size()
	return nil
}

// Write dopisuje wpis do pliku, rotując go wcześniej, jeśli wpis by się nie zmieścił.
func (p *PlikZRotacja) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.plik == nil {
		if err := p.otworz(); err != nil {
			return 0, err
		}
	}
	// Rotujemy przed zapisem, żeby pojedynczy wpis nigdy nie został rozcięty
	// między dwa pliki — połówka linii JSON jest bezużyteczna dla czytającego.
	if p.rozmiar > 0 && p.rozmiar+int64(len(b)) > p.maxBajtow {
		if err := p.rotuj(); err != nil {
			return 0, err
		}
	}

	n, err := p.plik.Write(b)
	p.rozmiar += int64(n)
	return n, err
}

// rotuj przesuwa numerację kopii i otwiera nowy, pusty plik.
//
// Wywoływane z zajętym p.mu.
func (p *PlikZRotacja) rotuj() error {
	if err := p.plik.Close(); err != nil {
		return fmt.Errorf("logging: zamknięcie pliku logu przed rotacją: %w", err)
	}
	p.plik = nil

	if p.kopie == 0 {
		// Bez kopii rotacja sprowadza się do zaczęcia od nowa.
		if err := os.Remove(p.sciezka); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: usunięcie starego pliku logu: %w", err)
		}
		return p.otworz()
	}

	// Najstarsza kopia wypada poza zakres i znika.
	if err := os.Remove(p.nazwaKopii(p.kopie)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logging: usunięcie najstarszej kopii logu: %w", err)
	}
	// Reszta przesuwa się o jeden numer w górę: .2 → .3, .1 → .2.
	for i := p.kopie - 1; i >= 1; i-- {
		if err := os.Rename(p.nazwaKopii(i), p.nazwaKopii(i+1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("logging: przesunięcie kopii logu %d: %w", i, err)
		}
	}
	if err := os.Rename(p.sciezka, p.nazwaKopii(1)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("logging: odłożenie bieżącego logu jako kopii: %w", err)
	}
	return p.otworz()
}

func (p *PlikZRotacja) nazwaKopii(n int) string {
	return fmt.Sprintf("%s.%d", p.sciezka, n)
}

// Close zamyka plik logu.
func (p *PlikZRotacja) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.plik == nil {
		return nil
	}
	err := p.plik.Close()
	p.plik = nil
	return err
}
