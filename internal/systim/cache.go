package systim

import (
	"context"
	"strings"
	"sync"
	"time"
)

// DomyslnyTTLKartotek to czas życia wpisu w cache kartotek.
//
// Kartoteki zmieniają się rzadko — nowego kontrahenta zakłada się raz na jakiś
// czas — a każdy odczyt to pełne pobranie całej kartoteki z API, bo metody
// listujące Systim nie przyjmują parametru wyszukiwania. Kilka minut jest
// kompromisem między liczbą wywołań a świeżością; rekord dodany w panelu przed
// chwilą i tak zostanie znaleziony, bo brak trafienia wymusza odświeżenie.
const DomyslnyTTLKartotek = 5 * time.Minute

// Klucze wpisów w cache. Odpowiadają metodom API.
const (
	kluczKontrahenci = "listCompanies"
	kluczProdukty    = "listProducts"
)

// Kartoteka to odczytana kartoteka wraz z indeksem po ID.
//
// Rekordy i indeks są współdzielone z cache — wywołujący może je czytać, ale nie
// wolno mu ich modyfikować.
type Kartoteka struct {
	// Rekordy w kolejności zwróconej przez API.
	Rekordy []Rekord
	// PoID indeksuje rekordy po ich ID, żeby odczyt jednego rekordu nie wymagał
	// przechodzenia całej listy.
	PoID map[string]Rekord
	// ZCache mówi, czy dane pochodzą z cache, a nie ze świeżego wywołania API.
	// Dzięki temu wywołujący wie, czy przy braku trafienia warto jeszcze raz
	// odpytać Systim, czy właśnie to zrobił.
	ZCache bool
}

// wpisKartoteki to jedna kartoteka w cache.
//
// mu serializuje pobieranie: gdy kilka żądań trafi na wygasły wpis naraz,
// kartotekę pobiera jedno z nich, a pozostałe czekają i korzystają z wyniku.
type wpisKartoteki struct {
	mu      sync.Mutex
	rekordy []Rekord
	poID    map[string]Rekord
	pobrane time.Time
}

// cacheKartotek trzyma kartoteki pobrane z Systim.
//
// Cache żyje w pamięci procesu, a nie w bazie: przy wielu replikach każda grzeje
// się osobno, co jest w porządku, bo to tylko oszczędność wywołań. Ze
// Stateless = true nie koliduje — to nie jest stan sesji MCP, tylko pamięć
// podręczna odczytów.
type cacheKartotek struct {
	ttl time.Duration
	// terazFn pozwala testom sterować upływem czasu.
	terazFn func() time.Time

	mu    sync.Mutex
	wpisy map[string]*wpisKartoteki
}

// nowyCacheKartotek tworzy cache o podanym TTL. TTL <= 0 wyłącza cache.
func nowyCacheKartotek(ttl time.Duration) *cacheKartotek {
	return &cacheKartotek{ttl: ttl, wpisy: make(map[string]*wpisKartoteki, 2)}
}

func (c *cacheKartotek) teraz() time.Time {
	if c.terazFn != nil {
		return c.terazFn()
	}
	return time.Now()
}

// wpis zwraca wpis o podanym kluczu, tworząc go przy pierwszym użyciu.
func (c *cacheKartotek) wpis(klucz string) *wpisKartoteki {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.wpisy[klucz]
	if !ok {
		w = &wpisKartoteki{}
		c.wpisy[klucz] = w
	}
	return w
}

// pobierz zwraca kartotekę z cache albo ze źródła.
//
// odswiez wymusza pobranie ze źródła niezależnie od wieku wpisu.
func (c *cacheKartotek) pobierz(ctx context.Context, klucz string, odswiez bool, zrodlo func(context.Context) ([]Rekord, error)) (Kartoteka, error) {
	if c == nil || c.ttl <= 0 {
		rekordy, err := zrodlo(ctx)
		if err != nil {
			return Kartoteka{}, err
		}
		return Kartoteka{Rekordy: rekordy, PoID: indeksPoID(rekordy)}, nil
	}

	w := c.wpis(klucz)
	w.mu.Lock()
	defer w.mu.Unlock()

	// Wpis mógł zostać odświeżony przez inne żądanie, gdy czekaliśmy na blokadę.
	if !odswiez && !w.pobrane.IsZero() && c.teraz().Sub(w.pobrane) < c.ttl {
		return Kartoteka{Rekordy: w.rekordy, PoID: w.poID, ZCache: true}, nil
	}
	// Czekanie na blokadę mogło trwać dłużej niż cierpliwość wywołującego.
	if err := ctx.Err(); err != nil {
		return Kartoteka{}, err
	}

	rekordy, err := zrodlo(ctx)
	if err != nil {
		return Kartoteka{}, err
	}
	w.rekordy = rekordy
	w.poID = indeksPoID(rekordy)
	w.pobrane = c.teraz()
	return Kartoteka{Rekordy: w.rekordy, PoID: w.poID}, nil
}

// indeksPoID buduje mapę ID → rekord. Przy powtórzonym ID wygrywa pierwszy
// rekord, czyli ten, który API zwróciło wcześniej.
func indeksPoID(rekordy []Rekord) map[string]Rekord {
	m := make(map[string]Rekord, len(rekordy))
	for _, r := range rekordy {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		if _, ok := m[id]; !ok {
			m[id] = r
		}
	}
	return m
}

// UstawCzasCacheDoTestow podmienia źródło czasu cache. Służy wyłącznie testom,
// które muszą przewinąć zegar poza TTL.
func (c *Client) UstawCzasCacheDoTestow(f func() time.Time) {
	c.kartoteki.terazFn = f
}
