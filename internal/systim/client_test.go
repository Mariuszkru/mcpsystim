package systim

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serwerTestowy udaje endpoint /jsonAPI. Handler dostaje rozparsowane pole "act"
// oraz całe ciało form-urlencoded.
func serwerTestowy(t *testing.T, h func(act string, form url.Values, w http.ResponseWriter)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("metoda = %s, chcę POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Errorf("Content-Type = %q, chcę application/x-www-form-urlencoded", ct)
		}
		ciało, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("odczyt ciała: %v", err)
		}
		form, err := url.ParseQuery(string(ciało))
		if err != nil {
			t.Fatalf("parsowanie ciała %q: %v", ciało, err)
		}
		w.Header().Set("Content-Type", "application/json")
		h(form.Get("act"), form, w)
	}))
	t.Cleanup(s.Close)
	return s
}

func klientDo(t *testing.T, s *httptest.Server) *Client {
	t.Helper()
	c, err := NewClient(Opcje{
		Login:   "api_user",
		Pass:    "tajne_haslo",
		BaseURL: s.URL,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient = %v", err)
	}
	return c
}

func TestBlad13PowodujePrzelogowanieIPonowienie(t *testing.T) {
	var logowania, proby atomic.Int32

	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			n := logowania.Add(1)
			if form.Get("login") != "api_user" || form.Get("pass") != "tajne_haslo" {
				t.Errorf("logowanie bez poprawnych danych: %v", form)
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"TOKEN_`+string(rune('A'+n-1))+`"}}`)
		case "listCompanies":
			n := proby.Add(1)
			if n == 1 {
				// Pierwsza próba: sesja wygasła.
				io.WriteString(w, `{"error":{"code":13,"message":"Brak sesji uzytkownika"},"result":null}`)
				return
			}
			// Po przelogowaniu musi przyjść świeży token, nie ten sam co poprzednio.
			if form.Get("token") != "TOKEN_B" {
				t.Errorf("ponowienie z tokenem %q, chcę TOKEN_B", form.Get("token"))
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"41":{"nazwa":"Alfa"}}}`)
		default:
			t.Errorf("nieoczekiwana metoda %q", act)
		}
	})

	c := klientDo(t, s)
	rekordy, err := c.ListCompanies(context.Background())
	if err != nil {
		t.Fatalf("ListCompanies = %v, chcę sukcesu po automatycznym przelogowaniu", err)
	}
	if len(rekordy) != 1 || rekordy[0].Nazwa() != "Alfa" {
		t.Errorf("rekordy = %+v, chcę jednego kontrahenta Alfa", rekordy)
	}
	if got := logowania.Load(); got != 2 {
		t.Errorf("logowań = %d, chcę 2 (pierwsze na starcie, drugie po błędzie 13)", got)
	}
	if got := proby.Load(); got != 2 {
		t.Errorf("prób listCompanies = %d, chcę 2 (oryginał + jedno ponowienie)", got)
	}
}

func TestBlad13DwaRazyZRzeduNieWpadaWPetle(t *testing.T) {
	var logowania, proby atomic.Int32

	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			logowania.Add(1)
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		case "listCompanies":
			proby.Add(1)
			// Zawsze 13 — np. ktoś trzyma otwarty panel WWW i kasuje sesje API na bieżąco.
			io.WriteString(w, `{"error":{"code":"13","message":"Brak sesji uzytkownika"},"result":null}`)
		}
	})

	c := klientDo(t, s)
	_, err := c.ListCompanies(context.Background())
	if err == nil {
		t.Fatal("ListCompanies = nil, chcę błędu po dwóch nieudanych próbach")
	}
	if !errors.Is(err, ErrBrakSesji) {
		t.Errorf("err = %v, chcę żeby errors.Is(err, ErrBrakSesji) było prawdą", err)
	}
	if got := proby.Load(); got != 2 {
		t.Errorf("prób = %d, chcę dokładnie 2 — jedno ponowienie, bez pętli", got)
	}
	if got := logowania.Load(); got != 2 {
		t.Errorf("logowań = %d, chcę 2", got)
	}
}

func TestRownolegleWywolaniaNaWygaslymTokenieLogujaSieRaz(t *testing.T) {
	// Scenariusz z życia: użytkownik zalogował się do panelu WWW, co skasowało
	// wszystkie sesje API, a w tym momencie leci kilka narzędzi naraz.
	const rownoleglych = 16

	var logowania atomic.Int32
	var mu sync.Mutex
	// Żaden token nie jest ważny na starcie — sesje API zostały właśnie skasowane
	// przez zalogowanie użytkownika do panelu WWW.
	wazneTokeny := map[string]bool{}

	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			n := logowania.Add(1)
			// Logowanie celowo powolne — poszerza okno na wyścig.
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			// Nowe logowanie unieważnia poprzednie tokeny, dokładnie jak Systim.
			wazneTokeny = map[string]bool{"NOWY": true}
			mu.Unlock()
			_ = n
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"NOWY"}}`)
		case "listProducts":
			mu.Lock()
			ok := wazneTokeny[form.Get("token")]
			mu.Unlock()
			if !ok {
				io.WriteString(w, `{"error":{"code":13,"message":"Brak sesji uzytkownika"},"result":null}`)
				return
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":[]}`)
		}
	})

	c := klientDo(t, s)
	// Zaczynamy z tokenem, który serwer zaraz odrzuci — bez wywoływania logowania na starcie.
	c.UstawTokenDoTestow("STARY")

	var wg sync.WaitGroup
	bledy := make([]error, rownoleglych)
	for i := range rownoleglych {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.ListProducts(context.Background())
			bledy[i] = err
		}()
	}
	wg.Wait()

	for i, err := range bledy {
		if err != nil {
			t.Errorf("goroutine %d: ListProducts = %v, chcę sukcesu", i, err)
		}
	}
	if got := logowania.Load(); got != 1 {
		t.Errorf("logowań = %d, chcę dokładnie 1 — %d równoległych błędów 13 musi dać jedno logowanie",
			got, rownoleglych)
	}
}

func TestPierwszeWywolanieLogujeSieRazDlaWieluGoroutine(t *testing.T) {
	// Zimny start: token jest pusty, a narzędzia ruszają równolegle.
	var logowania atomic.Int32
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			logowania.Add(1)
			time.Sleep(20 * time.Millisecond)
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		case "listVatRates":
			if form.Get("token") != "T" {
				t.Errorf("token = %q, chcę T", form.Get("token"))
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"1":{"nazwa":"23%"}}}`)
		}
	})

	c := klientDo(t, s)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.ListVatRates(context.Background()); err != nil {
				t.Errorf("ListVatRates = %v", err)
			}
		}()
	}
	wg.Wait()

	if got := logowania.Load(); got != 1 {
		t.Errorf("logowań = %d, chcę 1", got)
	}
}

func TestBlednyLoginNiePonawiaSie(t *testing.T) {
	var logowania atomic.Int32
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		logowania.Add(1)
		io.WriteString(w, `{"error":{"code":4,"message":"Nieprawidlowy login lub haslo"},"result":null}`)
	})

	c := klientDo(t, s)
	_, err := c.ListCompanies(context.Background())
	if !errors.Is(err, ErrBledneDaneLog) {
		t.Fatalf("err = %v, chcę ErrBledneDaneLog", err)
	}
	if got := logowania.Load(); got != 1 {
		t.Errorf("prób logowania = %d, chcę 1 — błędne hasło nie może być ponawiane", got)
	}
}

func TestPustyWynikNieJestBledemDlaMetodListujacych(t *testing.T) {
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		default:
			io.WriteString(w, `{"error":{"code":8,"message":"Brak danych"},"result":[]}`)
		}
	})

	c := klientDo(t, s)
	rekordy, err := c.ListCompanies(context.Background())
	if err != nil {
		t.Fatalf("ListCompanies = %v, chcę nil — kod 8 dla listy to pusty wynik, nie awaria", err)
	}
	if len(rekordy) != 0 {
		t.Errorf("rekordy = %+v, chcę pustej listy", rekordy)
	}
}

func TestBladMiesiacZamknietyDocieraZKomunikatem(t *testing.T) {
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		default:
			io.WriteString(w, `{"error":{"code":16,"message":"Miesiac jest zamkniety","fields":["data_wystawienia"]},"result":null}`)
		}
	})

	c := klientDo(t, s)
	_, err := c.Wywolaj(context.Background(), "addSellInvoice", url.Values{})
	if !errors.Is(err, ErrMiesiacZamkniety) {
		t.Fatalf("err = %v, chcę ErrMiesiacZamkniety", err)
	}
	var se *SystimError
	if !errors.As(err, &se) {
		t.Fatal("errors.As nie zwrócił *SystimError")
	}
	if len(se.Fields) != 1 || se.Fields[0] != "data_wystawienia" {
		t.Errorf("Fields = %v, chcę [data_wystawienia]", se.Fields)
	}
	if !contains(KomunikatPL(err), "okres księgowy") {
		t.Errorf("KomunikatPL = %q, chcę wzmianki o zamkniętym okresie", KomunikatPL(err))
	}
}

func TestMaskowanieHaslaITokenuWLogach(t *testing.T) {
	form := url.Values{}
	form.Set("act", "addSellInvoice")
	form.Set("login", "api_user")
	form.Set("pass", "tajne_haslo")
	form.Set("token", "SEKRETNY_TOKEN")
	form.Set("id_kontrahenta", "41")

	got := zamaskujParametry(form)
	for _, zakazane := range []string{"tajne_haslo", "SEKRETNY_TOKEN", "api_user"} {
		if contains(got, zakazane) {
			t.Errorf("log %q zawiera wartość wrażliwą %q", got, zakazane)
		}
	}
	if !contains(got, "id_kontrahenta=41") {
		t.Errorf("log %q gubi pola niewrażliwe", got)
	}
}

func TestHTTP500ZwracaCzytelnyBlad(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "<html>502 Bad Gateway</html>")
	}))
	defer s.Close()

	c := klientDo(t, s)
	_, err := c.ListCompanies(context.Background())
	if err == nil {
		t.Fatal("err = nil, chcę błędu przy HTTP 502")
	}
	if !contains(err.Error(), "502") {
		t.Errorf("err = %v, chcę kodu HTTP w komunikacie", err)
	}
}
