package systim

import (
	"encoding/json"
	"errors"
	"testing"
)

// Testy w tym pliku operują na surowych stringach JSON, bo to właśnie zmienny
// kształt odpowiedzi PHP-owego backendu jest miejscem, w którym ta integracja się psuje.

func TestFlexIntPrzyjmujeLiczbeIString(t *testing.T) {
	przypadki := []struct {
		nazwa string
		json  string
		chce  int
	}{
		{"liczba zero", `{"code": 0}`, 0},
		{"string zero", `{"code": "0"}`, 0},
		{"liczba dodatnia", `{"code": 13}`, 13},
		{"string dodatni", `{"code": "13"}`, 13},
		{"null", `{"code": null}`, 0},
		{"pusty string", `{"code": ""}`, 0},
		{"string z częścią dziesiętną", `{"code": "6.00"}`, 6},
		{"pole nieobecne", `{}`, 0},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			var v struct {
				Code FlexInt `json:"code"`
			}
			if err := json.Unmarshal([]byte(p.json), &v); err != nil {
				t.Fatalf("Unmarshal(%s) = %v, chcę nil", p.json, err)
			}
			if v.Code.Int() != p.chce {
				t.Errorf("code = %d, chcę %d", v.Code.Int(), p.chce)
			}
		})
	}
}

func TestOdpowiedzZKodemBleduJakoString(t *testing.T) {
	// Ten sam błąd raz przychodzi jako liczba, raz jako string. Oba warianty muszą
	// dać ten sam SystimError, żeby errors.Is działało niezależnie od kaprysu backendu.
	warianty := []string{
		`{"error":{"code":13,"message":"Brak sesji uzytkownika"},"result":null}`,
		`{"error":{"code":"13","message":"Brak sesji uzytkownika"},"result":null}`,
	}
	for _, w := range warianty {
		var r Response
		if err := json.Unmarshal([]byte(w), &r); err != nil {
			t.Fatalf("Unmarshal(%s) = %v", w, err)
		}
		if r.Error.Code.Int() != KodBrakSesji {
			t.Errorf("kod = %d, chcę %d (dla %s)", r.Error.Code.Int(), KodBrakSesji, w)
		}
		err := &SystimError{Code: r.Error.Code.Int()}
		if !errors.Is(err, ErrBrakSesji) {
			t.Errorf("errors.Is(err, ErrBrakSesji) = false dla %s", w)
		}
	}
}

func TestFlexStringsZnosiTabliceMapeIBrak(t *testing.T) {
	przypadki := []struct {
		nazwa string
		json  string
		ile   int
	}{
		{"tablica", `{"fields":["nip","nazwa"]}`, 2},
		{"mapa asocjacyjna", `{"fields":{"nip":"Niepoprawny NIP","nazwa":"Wymagane"}}`, 2},
		{"pole nieobecne", `{}`, 0},
		{"null", `{"fields":null}`, 0},
		{"pusta tablica", `{"fields":[]}`, 0},
		{"pojedynczy string", `{"fields":"nip"}`, 1},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			var v struct {
				Fields FlexStrings `json:"fields"`
			}
			if err := json.Unmarshal([]byte(p.json), &v); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", p.json, err)
			}
			if len(v.Fields) != p.ile {
				t.Errorf("fields = %v (len %d), chcę %d elementów", v.Fields, len(v.Fields), p.ile)
			}
		})
	}
}

func TestNullableDecimalZnosiPustyString(t *testing.T) {
	przypadki := []struct {
		nazwa     string
		json      string
		ustawione bool
		wartosc   string
	}{
		{"pusty string zamiast null", `{"kwota":""}`, false, ""},
		{"null", `{"kwota":null}`, false, ""},
		{"pole nieobecne", `{}`, false, ""},
		{"liczba", `{"kwota":123.45}`, true, "123.45"},
		{"string z kropką", `{"kwota":"123.45"}`, true, "123.45"},
		{"string z przecinkiem", `{"kwota":"123,45"}`, true, "123.45"},
		{"zero jako string", `{"kwota":"0"}`, true, "0"},
		{"kwota ujemna", `{"kwota":"-10.50"}`, true, "-10.5"},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			var v struct {
				Kwota NullableDecimal `json:"kwota"`
			}
			if err := json.Unmarshal([]byte(p.json), &v); err != nil {
				t.Fatalf("Unmarshal(%s) = %v, chcę nil — pusty string nie może wywracać dekodowania", p.json, err)
			}
			if v.Kwota.Ustawione != p.ustawione {
				t.Errorf("Ustawione = %v, chcę %v", v.Kwota.Ustawione, p.ustawione)
			}
			if p.ustawione && v.Kwota.Wartosc.String() != p.wartosc {
				t.Errorf("wartość = %s, chcę %s", v.Kwota.Wartosc.String(), p.wartosc)
			}
		})
	}
}

func TestNullableIntZnosiPustyString(t *testing.T) {
	przypadki := []struct {
		nazwa     string
		json      string
		ustawione bool
		wartosc   int
	}{
		{"pusty string", `{"n":""}`, false, 0},
		{"null", `{"n":null}`, false, 0},
		{"liczba", `{"n":7}`, true, 7},
		{"string", `{"n":"7"}`, true, 7},
		{"zero", `{"n":0}`, true, 0},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			var v struct {
				N NullableInt `json:"n"`
			}
			if err := json.Unmarshal([]byte(p.json), &v); err != nil {
				t.Fatalf("Unmarshal(%s) = %v", p.json, err)
			}
			if v.N.Ustawione != p.ustawione || v.N.Wartosc != p.wartosc {
				t.Errorf("= %+v, chcę {Wartosc:%d Ustawione:%v}", v.N, p.wartosc, p.ustawione)
			}
		})
	}
}

func TestHTMLStringOdkodowujeEncje(t *testing.T) {
	// Systim przepuszcza stringi przez PHP-owe htmlspecialchars(). Nazwa kontrahenta
	// z ampersandem albo cudzysłowem musi wrócić do postaci czytelnej dla człowieka.
	przypadki := []struct {
		json string
		chce string
	}{
		{`{"nazwa":"Kowalski &amp; Synowie sp. z o.o."}`, `Kowalski & Synowie sp. z o.o.`},
		{`{"nazwa":"Firma &quot;ABC&quot;"}`, `Firma "ABC"`},
		{`{"nazwa":"Zaopatrzenie &lt;Hurt&gt;"}`, `Zaopatrzenie <Hurt>`},
		{`{"nazwa":"Piek&aacute;rnia"}`, `Piekárnia`},
		{`{"nazwa":"J&#039;Adore"}`, `J'Adore`},
		{`{"nazwa":"Bez encji"}`, `Bez encji`},
	}
	for _, p := range przypadki {
		var v struct {
			Nazwa HTMLString `json:"nazwa"`
		}
		if err := json.Unmarshal([]byte(p.json), &v); err != nil {
			t.Fatalf("Unmarshal(%s) = %v", p.json, err)
		}
		if v.Nazwa.String() != p.chce {
			t.Errorf("nazwa = %q, chcę %q", v.Nazwa.String(), p.chce)
		}
	}
}

func TestRekordOdkodowujeEncjeWNazwieKontrahenta(t *testing.T) {
	raw := json.RawMessage(`{"41":{"nazwa":"Kowalski &amp; Synowie","nip":"123-456-78-90"}}`)
	rekordy, err := dekodujListe[Rekord](raw)
	if err != nil {
		t.Fatalf("dekodujListe = %v", err)
	}
	if len(rekordy) != 1 {
		t.Fatalf("dostałem %d rekordów, chcę 1", len(rekordy))
	}
	if got := rekordy[0].Nazwa(); got != "Kowalski & Synowie" {
		t.Errorf("Nazwa() = %q, chcę %q", got, "Kowalski & Synowie")
	}
	if rekordy[0].ID != "41" {
		t.Errorf("ID = %q, chcę %q — ID musi pochodzić z klucza mapy", rekordy[0].ID, "41")
	}
}

func TestDekodujListeMapaKluczowanaIDIrazTablica(t *testing.T) {
	przypadki := []struct {
		nazwa string
		json  string
		ile   int
		ids   []string
	}{
		{
			nazwa: "mapa kluczowana ID rekordu",
			json:  `{"41":{"nazwa":"Alfa"},"7":{"nazwa":"Beta"}}`,
			ile:   2,
			// Klucze numeryczne sortujemy numerycznie, więc 7 przed 41.
			ids: []string{"7", "41"},
		},
		{
			nazwa: "tablica obiektów z własnym id",
			json:  `[{"id":"7","nazwa":"Beta"},{"id":"41","nazwa":"Alfa"}]`,
			ile:   2,
			ids:   []string{"7", "41"},
		},
		{
			nazwa: "pusta tablica zamiast pustej mapy",
			json:  `[]`,
			ile:   0,
		},
		{
			nazwa: "pusta mapa",
			json:  `{}`,
			ile:   0,
		},
		{
			nazwa: "null",
			json:  `null`,
			ile:   0,
		},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			rekordy, err := dekodujListe[Rekord](json.RawMessage(p.json))
			if err != nil {
				t.Fatalf("dekodujListe(%s) = %v", p.json, err)
			}
			if len(rekordy) != p.ile {
				t.Fatalf("dostałem %d rekordów, chcę %d", len(rekordy), p.ile)
			}
			for i, chce := range p.ids {
				if rekordy[i].ID != chce {
					t.Errorf("rekord[%d].ID = %q, chcę %q", i, rekordy[i].ID, chce)
				}
			}
		})
	}
}

func TestDekodujListeZPolamiLiczbowymiJakoPustyString(t *testing.T) {
	// Realistyczna odpowiedź listSellInvoices: rabat pusty, kwoty jako stringi.
	raw := json.RawMessage(`{"1001":{"numer":"FV\/1\/2026","kwota_brutto":"1230.00","rabat":"","termin":""}}`)
	rekordy, err := dekodujListe[Rekord](raw)
	if err != nil {
		t.Fatalf("dekodujListe = %v", err)
	}
	if len(rekordy) != 1 {
		t.Fatalf("dostałem %d rekordów, chcę 1", len(rekordy))
	}
	if got := rekordy[0].Pole("kwota_brutto"); got != "1230.00" {
		t.Errorf("kwota_brutto = %q, chcę %q", got, "1230.00")
	}
	if got := rekordy[0].Pole("rabat"); got != "" {
		t.Errorf("rabat = %q, chcę pusty", got)
	}
}

func TestDekodujListeOdrzucaSkalarnaMape(t *testing.T) {
	// {"1":"23%"} to nie jest lista rekordów. Lepiej głośny błąd niż ciche zgubienie danych.
	_, err := dekodujListe[Rekord](json.RawMessage(`{"1":"23%"}`))
	if err == nil {
		t.Fatal("dekodujListe = nil, chcę błędu przy mapie ze skalarnymi wartościami")
	}
}

func TestSystimErrorPorownywanieSentinelami(t *testing.T) {
	err := &SystimError{Code: KodMiesiacZamkniety, Message: "Miesiac jest zamkniety", Act: "addSellInvoice"}
	if !errors.Is(err, ErrMiesiacZamkniety) {
		t.Error("errors.Is(err, ErrMiesiacZamkniety) = false, chcę true")
	}
	if errors.Is(err, ErrBrakSesji) {
		t.Error("errors.Is(err, ErrBrakSesji) = true, chcę false")
	}
	// Komunikat dla użytkownika ma tłumaczyć, co zrobić, nie tylko powtarzać kod.
	if k := err.KomunikatPL(); k == "" || !contains(k, "okres księgowy") {
		t.Errorf("KomunikatPL() = %q, chcę wzmianki o zamkniętym okresie księgowym", k)
	}
	if k := (&SystimError{Code: KodDostepZabroniony}).KomunikatPL(); !contains(k, "throttling") {
		t.Errorf("KomunikatPL() dla kodu 2 = %q, chcę podpowiedzi o throttlingu", k)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
