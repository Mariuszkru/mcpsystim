package auth

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRownolegleZadaniaNaZimnyCacheNieDostajaFalszywego401 odtwarza sytuację
// z produkcji: po restarcie kontenera zestaw kluczy jest pusty, a żądania
// przychodzą kilka naraz.
//
// Pierwsze z nich pobiera JWKS, pozostałe czekają na blokadzie. Jeżeli po
// wejściu do sekcji krytycznej sprawdza się stan sprzed oczekiwania, a nie to,
// czy szukany kid już jest w cache, czekające żądania wpadają w limit
// częstotliwości odświeżeń i kończą się błędem „nieznany klucz podpisujący"
// ułamek milisekundy po udanym pobraniu tych właśnie kluczy.
func TestRownolegleZadaniaNaZimnyCacheNieDostajaFalszywego401(t *testing.T) {
	idp := nowyIdP(t)
	// Pobranie musi trwać na tyle długo, żeby pozostałe goroutine zdążyły ustawić
	// się w kolejce za pierwszą.
	idp.opoznienieJWKS = 100 * time.Millisecond

	z := NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil)

	const zadan = 8
	bledy := make(chan error, zadan)
	var start sync.WaitGroup
	var koniec sync.WaitGroup
	start.Add(1)

	for range zadan {
		koniec.Add(1)
		go func() {
			defer koniec.Done()
			start.Wait() // wszystkie ruszają w tym samym momencie
			if _, err := z.Klucz(context.Background(), idp.kid); err != nil {
				bledy <- err
			}
		}()
	}
	start.Done()
	koniec.Wait()
	close(bledy)

	for err := range bledy {
		t.Errorf("Klucz = %v, chcę sukcesu — klucz był w cache, zanim żądanie doszło do sprawdzenia", err)
	}
	// Blokada ma złożyć wszystkie żądania w jedno pobranie JWKS.
	if got := idp.pobrania.Load(); got != 1 {
		t.Errorf("pobrań JWKS = %d, chcę 1", got)
	}
}

// TestNieznanyKidNieDobijaIdP pilnuje, żeby poprawka nie zdjęła ochrony przed
// zalewem żądań: token z kidem, którego IdP nie zna, nadal ma powodować
// dokładnie jedno pobranie, a nie jedno na żądanie.
func TestNieznanyKidNieDobijaIdP(t *testing.T) {
	idp := nowyIdP(t)
	z := NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil)

	for i := range 5 {
		if _, err := z.Klucz(context.Background(), "kid-ktorego-nie-ma"); err == nil {
			t.Fatalf("próba %d: Klucz = nil, chcę błędu o nieznanym kluczu", i)
		}
	}
	if got := idp.pobrania.Load(); got != 1 {
		t.Errorf("pobrań JWKS = %d, chcę 1 — limit częstotliwości ma chronić IdP przed zalewem", got)
	}
}

// TestZnanyKidPoOdswiezeniuPrzechodziMimoLimitu sprawdza drugą stronę tej samej
// poprawki: gdy klucz jest już w cache, limit częstotliwości nie może blokować
// jego użycia, choćby nieudana próba odświeżenia była przed chwilą.
func TestZnanyKidPoOdswiezeniuPrzechodziMimoLimitu(t *testing.T) {
	idp := nowyIdP(t)
	z := NoweZrodloKluczy(idp.srv.URL, idp.srv.Client(), nil)

	// Nieznany kid wywołuje pobranie i ustawia znacznik ostatniej próby.
	if _, err := z.Klucz(context.Background(), "kid-ktorego-nie-ma"); err == nil {
		t.Fatal("Klucz = nil dla nieznanego kid, chcę błędu")
	}
	// Kid, który w pobranym zestawie jest, musi przejść od razu.
	if _, err := z.Klucz(context.Background(), idp.kid); err != nil {
		t.Errorf("Klucz = %v, chcę sukcesu — klucz jest w cache, limit go nie dotyczy", err)
	}
	if got := idp.pobrania.Load(); got != 1 {
		t.Errorf("pobrań JWKS = %d, chcę 1", got)
	}
}
