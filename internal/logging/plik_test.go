package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// zapisz wpisuje n bajtów jako jeden wpis logu.
func zapisz(t *testing.T, p *PlikZRotacja, n int) {
	t.Helper()
	if _, err := p.Write([]byte(strings.Repeat("x", n))); err != nil {
		t.Fatalf("Write = %v", err)
	}
}

func rozmiar(t *testing.T, sciezka string) int64 {
	t.Helper()
	info, err := os.Stat(sciezka)
	if err != nil {
		t.Fatalf("Stat %s = %v", sciezka, err)
	}
	return info.Size()
}

func TestZapisTworzyKatalogIPlik(t *testing.T) {
	sciezka := filepath.Join(t.TempDir(), "logi", "systim-mcp.log")
	p, err := NowyPlikZRotacja(sciezka, 1, 3)
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	defer p.Close()

	zapisz(t, p, 100)
	if got := rozmiar(t, sciezka); got != 100 {
		t.Errorf("rozmiar = %d, chcę 100", got)
	}
}

func TestRotacjaPoPrzekroczeniuRozmiaru(t *testing.T) {
	katalog := t.TempDir()
	sciezka := filepath.Join(katalog, "systim-mcp.log")
	// 1 MB limitu; wpisy po 400 KB, więc trzeci nie zmieści się już w pierwszym pliku.
	p, err := NowyPlikZRotacja(sciezka, 1, 3)
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	defer p.Close()

	const wpis = 400 << 10
	zapisz(t, p, wpis)
	zapisz(t, p, wpis)
	// Do tej pory 800 KB — bez rotacji.
	if _, err := os.Stat(sciezka + ".1"); !os.IsNotExist(err) {
		t.Fatalf("kopia .1 powstała przed przekroczeniem limitu")
	}

	zapisz(t, p, wpis)
	// Trzeci wpis nie mieścił się w limicie, więc poprzednia zawartość poszła do .1,
	// a bieżący plik zawiera wyłącznie ten wpis — nierozcięty.
	if got := rozmiar(t, sciezka); got != wpis {
		t.Errorf("rozmiar bieżącego pliku = %d, chcę %d (cały wpis w jednym pliku)", got, wpis)
	}
	if got := rozmiar(t, sciezka+".1"); got != 2*wpis {
		t.Errorf("rozmiar kopii .1 = %d, chcę %d", got, 2*wpis)
	}
}

func TestRotacjaPrzesuwaKopieIUsuwaNajstarsza(t *testing.T) {
	katalog := t.TempDir()
	sciezka := filepath.Join(katalog, "systim-mcp.log")
	p, err := NowyPlikZRotacja(sciezka, 1, 2) // trzymamy tylko .1 i .2
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	defer p.Close()

	// Każdy wpis zajmuje ponad połowę limitu, więc każdy kolejny wymusza rotację.
	const wpis = 600 << 10
	for range 4 {
		zapisz(t, p, wpis)
	}

	for _, plik := range []string{sciezka, sciezka + ".1", sciezka + ".2"} {
		if _, err := os.Stat(plik); err != nil {
			t.Errorf("brakuje pliku %s: %v", filepath.Base(plik), err)
		}
	}
	// .3 nigdy nie może powstać przy dwóch kopiach.
	if _, err := os.Stat(sciezka + ".3"); !os.IsNotExist(err) {
		t.Errorf("powstała kopia .3 mimo limitu 2 kopii")
	}
}

func TestBezKopiiRotacjaCzysciPlik(t *testing.T) {
	sciezka := filepath.Join(t.TempDir(), "systim-mcp.log")
	p, err := NowyPlikZRotacja(sciezka, 1, 0)
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	defer p.Close()

	const wpis = 600 << 10
	zapisz(t, p, wpis)
	zapisz(t, p, wpis)

	if got := rozmiar(t, sciezka); got != wpis {
		t.Errorf("rozmiar = %d, chcę %d — bez kopii rotacja zaczyna plik od nowa", got, wpis)
	}
	if _, err := os.Stat(sciezka + ".1"); !os.IsNotExist(err) {
		t.Errorf("powstała kopia .1 mimo zera kopii")
	}
}

func TestPonowneOtwarcieDopisujeIPamietaRozmiar(t *testing.T) {
	sciezka := filepath.Join(t.TempDir(), "systim-mcp.log")

	p, err := NowyPlikZRotacja(sciezka, 1, 3)
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	zapisz(t, p, 700<<10)
	if err := p.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}

	// Po restarcie serwera dopisujemy do istniejącego pliku, a rozmiar liczy się
	// od tego, co już w nim było — inaczej rotacja spóźniłaby się o poprzednią treść.
	p2, err := NowyPlikZRotacja(sciezka, 1, 3)
	if err != nil {
		t.Fatalf("ponowne NowyPlikZRotacja = %v", err)
	}
	defer p2.Close()

	zapisz(t, p2, 400<<10)
	if got := rozmiar(t, sciezka); got != 400<<10 {
		t.Errorf("rozmiar = %d, chcę %d — drugi wpis miał wymusić rotację", got, 400<<10)
	}
	if _, err := os.Stat(sciezka + ".1"); err != nil {
		t.Errorf("brak kopii .1 po rotacji wymuszonej rozmiarem z poprzedniego uruchomienia: %v", err)
	}
}

func TestRownolegleZapisyNieGubiaBajtow(t *testing.T) {
	sciezka := filepath.Join(t.TempDir(), "systim-mcp.log")
	p, err := NowyPlikZRotacja(sciezka, 64, 3) // limit tak duży, żeby rotacja nie zaszła
	if err != nil {
		t.Fatalf("NowyPlikZRotacja = %v", err)
	}
	defer p.Close()

	const goroutines, wpisow, dlugosc = 8, 50, 128
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range wpisow {
				// Nie zapisz(): t.Fatalf poza goroutine testu nie zatrzymuje testu.
				if _, err := p.Write([]byte(strings.Repeat("x", dlugosc))); err != nil {
					t.Errorf("Write = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	chce := int64(goroutines * wpisow * dlugosc)
	if got := rozmiar(t, sciezka); got != chce {
		t.Errorf("rozmiar = %d, chcę %d", got, chce)
	}
}

func TestOdrzucaBledneUstawienia(t *testing.T) {
	katalog := t.TempDir()
	przypadki := []struct {
		nazwa   string
		sciezka string
		maxMB   int
		kopie   int
	}{
		{"pusta ścieżka", "", 10, 3},
		{"zerowy rozmiar", filepath.Join(katalog, "a.log"), 0, 3},
		{"ujemna liczba kopii", filepath.Join(katalog, "b.log"), 10, -1},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			if _, err := NowyPlikZRotacja(p.sciezka, p.maxMB, p.kopie); err == nil {
				t.Error("NowyPlikZRotacja = nil, chcę błędu")
			}
		})
	}
}
