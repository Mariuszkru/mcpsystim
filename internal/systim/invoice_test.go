package systim

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal {
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

func przykladoweZadanie() ZadanieFaktury {
	return ZadanieFaktury{
		IDKontrahenta:   "41",
		DataWystawienia: "2026-07-24",
		DataSprzedazy:   "2026-07-24",
		Rodzaj:          RodzajFakturaVAT,
		IDSzablonu:      "3",
		IDNumeracji:     "5",
		Pozycje: []PozycjaFaktury{
			{
				Opis:        "Konsultacje",
				Ilosc:       d("10"),
				Jednostka:   "h",
				CenaNetto:   d("200"),
				KwotaNetto:  d("2000"),
				StawkaVatID: "1",
				KwotaVat:    d("460"),
				KwotaBrutto: d("2460"),
			},
			{
				IDProduktu:  "88",
				Opis:        "Wdrożenie",
				Ilosc:       d("1"),
				Jednostka:   "szt",
				CenaNetto:   d("500"),
				KwotaNetto:  d("500"),
				StawkaVatID: "1",
				KwotaVat:    d("115"),
				KwotaBrutto: d("615"),
			},
		},
	}
}

func TestBudujParametryFakturyUzywaTablicPHPaNieJSONa(t *testing.T) {
	v, err := BudujParametryFaktury(przykladoweZadanie())
	if err != nil {
		t.Fatalf("BudujParametryFaktury = %v", err)
	}

	// Pozycje muszą jechać jako równoległe tablice w konwencji PHP.
	chciane := map[string]string{
		"id_kontrahenta":   "41",
		"data_wystawienia": "2026-07-24",
		"data_sprzedazy":   "2026-07-24",
		"rodzaj":           "0",
		"id_szablonu":      "3",
		"id_numeracji":     "5",

		"opis[0]":         "Konsultacje",
		"ilosc[0]":        "10",
		"jednostka[0]":    "h",
		"cena_netto[0]":   "200",
		"kwota_netto[0]":  "2000.00",
		"stawka_vat[0]":   "1",
		"kwota_vat[0]":    "460.00",
		"kwota_brutto[0]": "2460.00",

		"id_produktu[1]":  "88",
		"opis[1]":         "Wdrożenie",
		"ilosc[1]":        "1",
		"kwota_netto[1]":  "500.00",
		"kwota_brutto[1]": "615.00",
	}
	for k, chce := range chciane {
		if got := v.Get(k); got != chce {
			t.Errorf("%s = %q, chcę %q", k, got, chce)
		}
	}

	// Pierwsza pozycja nie ma id_produktu — pole nie może się pojawić pusto.
	if _, ok := v["id_produktu[0]"]; ok {
		t.Error("id_produktu[0] jest obecne, a pozycja nie miała produktu z kartoteki")
	}

	// Zakodowane ciało to form-urlencoded, nie JSON. Nawiasy lecą procentowo.
	ciało := v.Encode()
	if strings.HasPrefix(strings.TrimSpace(ciało), "{") {
		t.Fatalf("ciało wygląda na JSON: %s", ciało)
	}
	if !strings.Contains(ciało, "opis%5B0%5D=Konsultacje") {
		t.Errorf("ciało nie zawiera zakodowanego opis[0]: %s", ciało)
	}

	// Po rozkodowaniu (tak jak zrobi to PHP) klucze wracają do postaci z nawiasami.
	odkodowane, err := url.ParseQuery(ciało)
	if err != nil {
		t.Fatalf("ParseQuery = %v", err)
	}
	if odkodowane.Get("opis[0]") != "Konsultacje" || odkodowane.Get("opis[1]") != "Wdrożenie" {
		t.Errorf("po rozkodowaniu opis[0]=%q opis[1]=%q", odkodowane.Get("opis[0]"), odkodowane.Get("opis[1]"))
	}
}

func TestBudujParametryFakturyPolaOpcjonalne(t *testing.T) {
	z := przykladoweZadanie()
	z.TerminPlatnosci = 14
	z.FormaPlatnosci = "przelew"
	z.Uwagi = "Płatne na konto firmowe"
	z.Rabat = "5"
	z.WyslijEmail = true
	z.EmailAdres = "ksiegowosc@example.com"
	z.WyslijDoKSeF = true

	v, err := BudujParametryFaktury(z)
	if err != nil {
		t.Fatalf("BudujParametryFaktury = %v", err)
	}
	chciane := map[string]string{
		"termin_platnosci": "14",
		"forma_platnosci":  "przelew",
		"uwagi":            "Płatne na konto firmowe",
		"rabat":            "5",
		"email_wyslij":     "1",
		"email_adres":      "ksiegowosc@example.com",
		"wyslij_do_ksef":   "1",
	}
	for k, chce := range chciane {
		if got := v.Get(k); got != chce {
			t.Errorf("%s = %q, chcę %q", k, got, chce)
		}
	}
}

func TestBudujParametryFakturyPolaOpcjonalnePomijaneGdyPuste(t *testing.T) {
	v, err := BudujParametryFaktury(przykladoweZadanie())
	if err != nil {
		t.Fatalf("BudujParametryFaktury = %v", err)
	}
	for _, k := range []string{"termin_platnosci", "forma_platnosci", "uwagi", "rabat", "email_wyslij", "wyslij_do_ksef"} {
		if _, ok := v[k]; ok {
			t.Errorf("pole %q zostało wysłane mimo braku wartości", k)
		}
	}
}

func TestBudujParametryFakturyWymaganePolaBraki(t *testing.T) {
	przypadki := []struct {
		nazwa    string
		zmien    func(*ZadanieFaktury)
		fragment string
	}{
		{"brak id_szablonu", func(z *ZadanieFaktury) { z.IDSzablonu = "" }, "id_szablonu"},
		{"brak id_numeracji", func(z *ZadanieFaktury) { z.IDNumeracji = "" }, "id_numeracji"},
		{"brak id_kontrahenta", func(z *ZadanieFaktury) { z.IDKontrahenta = "" }, "id_kontrahenta"},
		{"brak daty wystawienia", func(z *ZadanieFaktury) { z.DataWystawienia = "" }, "data_wystawienia"},
		{"brak daty sprzedaży", func(z *ZadanieFaktury) { z.DataSprzedazy = "" }, "data_sprzedazy"},
		{"brak pozycji", func(z *ZadanieFaktury) { z.Pozycje = nil }, "pozycję"},
		{"pozycja bez ID stawki VAT", func(z *ZadanieFaktury) { z.Pozycje[0].StawkaVatID = "" }, "stawki VAT"},
		{"wysyłka e-mail bez adresu", func(z *ZadanieFaktury) { z.WyslijEmail = true }, "adresu e-mail"},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			z := przykladoweZadanie()
			p.zmien(&z)
			_, err := BudujParametryFaktury(z)
			if err == nil {
				t.Fatalf("BudujParametryFaktury = nil, chcę błędu")
			}
			if !strings.Contains(err.Error(), p.fragment) {
				t.Errorf("err = %v, chcę wzmianki o %q", err, p.fragment)
			}
		})
	}
}

func TestRodzajeWalutoweSaJawnieOdrzucane(t *testing.T) {
	for _, rodzaj := range []int{23, 25, 29, 43, 44} {
		z := przykladoweZadanie()
		z.Rodzaj = rodzaj
		_, err := BudujParametryFaktury(z)
		if err == nil {
			t.Errorf("rodzaj %d został przyjęty, chcę jawnego odrzucenia", rodzaj)
			continue
		}
		if !strings.Contains(err.Error(), "walut obcych nie jest") {
			t.Errorf("rodzaj %d: err = %v, chcę komunikatu o braku obsługi walut obcych", rodzaj, err)
		}
	}
}

func TestRodzajeObslugiwanePrzechodza(t *testing.T) {
	for _, rodzaj := range []int{RodzajFakturaVAT, RodzajProForma, RodzajRachunek, RodzajParagonFiskalny, RodzajParagonNiefisk, RodzajOferta} {
		z := przykladoweZadanie()
		z.Rodzaj = rodzaj
		if _, err := BudujParametryFaktury(z); err != nil {
			t.Errorf("rodzaj %d (%s) odrzucony: %v", rodzaj, NazwaRodzaju(rodzaj), err)
		}
	}
	if _, err := BudujParametryFaktury(ZadanieFaktury{Rodzaj: 999}); err == nil {
		t.Error("nieznany rodzaj 999 został przyjęty")
	}
}

func TestAddSellInvoiceWysylaFormUrlencodedIczytaWynik(t *testing.T) {
	var zapamietaneCialo string

	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		case "addSellInvoice":
			zapamietaneCialo = form.Encode()
			if form.Get("opis[0]") != "Konsultacje" {
				t.Errorf("serwer nie widzi opis[0]; dostał: %v", form)
			}
			if form.Get("token") != "T" {
				t.Errorf("token = %q, chcę T", form.Get("token"))
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"id":"1001","numer":"FV\/12\/2026","result_code":"102"}}`)
		}
	})

	c := klientDo(t, s)
	wynik, err := c.AddSellInvoice(context.Background(), przykladoweZadanie())
	if err != nil {
		t.Fatalf("AddSellInvoice = %v", err)
	}
	if wynik.ID != "1001" {
		t.Errorf("ID = %q, chcę 1001", wynik.ID)
	}
	if wynik.Numer != "FV/12/2026" {
		t.Errorf("Numer = %q, chcę FV/12/2026", wynik.Numer)
	}
	// result_code przyszedł jako string — FlexInt musi to znieść.
	if wynik.ResultCode != ResultKsiegowanieNieudane {
		t.Errorf("ResultCode = %d, chcę 102", wynik.ResultCode)
	}
	opis, wymagaUwagi := OpisResultCode(wynik.ResultCode)
	if !wymagaUwagi {
		t.Error("result_code 102 nie został oznaczony jako wymagający uwagi użytkownika")
	}
	if !strings.Contains(opis, "NIE powiodło") {
		t.Errorf("opis = %q, chcę wyraźnej informacji o nieudanym księgowaniu", opis)
	}
	if strings.Contains(zapamietaneCialo, `"opis"`) {
		t.Errorf("ciało wygląda na JSON: %s", zapamietaneCialo)
	}
}

func TestAddSellInvoiceMiesiacZamkniety(t *testing.T) {
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		case "addSellInvoice":
			io.WriteString(w, `{"error":{"code":16,"message":"Miesiac jest zamkniety"},"result":null}`)
		}
	})
	c := klientDo(t, s)
	_, err := c.AddSellInvoice(context.Background(), przykladoweZadanie())
	if !errors.Is(err, ErrMiesiacZamkniety) {
		t.Fatalf("err = %v, chcę ErrMiesiacZamkniety", err)
	}
}

func TestOpisResultCode(t *testing.T) {
	przypadki := []struct {
		kod         int
		wymagaUwagi bool
	}{
		{ResultKsiegowanieWylaczone, false},
		{ResultZapisUtworzony, false},
		{ResultZapisZaktualizowany, false},
		{ResultKsiegowanieNieudane, true},
		{7777, true},
	}
	for _, p := range przypadki {
		opis, uwaga := OpisResultCode(p.kod)
		if opis == "" {
			t.Errorf("kod %d: pusty opis", p.kod)
		}
		if uwaga != p.wymagaUwagi {
			t.Errorf("kod %d: wymagaUwagi = %v, chcę %v", p.kod, uwaga, p.wymagaUwagi)
		}
	}
}

func TestGetSellInvoicePDF(t *testing.T) {
	s := serwerTestowy(t, func(act string, form url.Values, w http.ResponseWriter) {
		switch act {
		case "login":
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"token":"T"}}`)
		case "getSellInvoicePDF":
			if form.Get("id") != "1001" {
				t.Errorf("id = %q, chcę 1001", form.Get("id"))
			}
			io.WriteString(w, `{"error":{"code":0,"message":""},"result":{"file":"JVBERi0xLjQK","name":"FV_12_2026.pdf"}}`)
		}
	})
	c := klientDo(t, s)
	plik, err := c.GetSellInvoicePDF(context.Background(), "1001")
	if err != nil {
		t.Fatalf("GetSellInvoicePDF = %v", err)
	}
	if plik.Base64 != "JVBERi0xLjQK" || plik.Nazwa != "FV_12_2026.pdf" {
		t.Errorf("plik = %+v", plik)
	}
}
