package invoicing

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func stawkiTestowe(t *testing.T) *StawkiVAT {
	t.Helper()
	s, err := NoweStawkiVAT(map[string]int{"23": 1, "8": 2, "5": 3, "0": 4, "zw": 5})
	if err != nil {
		t.Fatalf("NoweStawkiVAT = %v", err)
	}
	return s
}

// TestZaokraglenieHalfAwayFromZero potwierdza założenie, na którym stoi cała
// arytmetyka dokumentu: decimal.Round zaokrągla połówki w górę co do wartości
// bezwzględnej, a nie do parzystej. Gdyby biblioteka zmieniła to zachowanie,
// kwoty na fakturach zaczęłyby się cicho rozjeżdżać o grosz.
func TestZaokraglenieHalfAwayFromZero(t *testing.T) {
	przypadki := []struct {
		wejscie string
		chce    string
	}{
		{"0.005", "0.01"},
		{"-0.005", "-0.01"},
		{"0.015", "0.02"},
		{"0.025", "0.03"}, // przy zaokrąglaniu do parzystej byłoby 0.02
		{"0.045", "0.05"}, // przy zaokrąglaniu do parzystej byłoby 0.04
		{"1.005", "1.01"},
		{"2.675", "2.68"},
		{"0.004", "0.00"},
		{"0.006", "0.01"},
	}
	for _, p := range przypadki {
		got := decimal.RequireFromString(p.wejscie).Round(2)
		if got.StringFixed(2) != p.chce {
			t.Errorf("Round(%s, 2) = %s, chcę %s", p.wejscie, got.StringFixed(2), p.chce)
		}
	}
}

func TestObliczTrzyRazy3333Przy23Procent(t *testing.T) {
	// Przypadek z zadania: 3 × 33.33 zł przy 23%.
	// netto  = 3 × 33.33          = 99.99
	// VAT    = 99.99 × 0.23       = 22.9977 → 23.00
	// brutto = 99.99 + 23.00      = 122.99
	pozycje, sumy, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "3", CenaNetto: "33.33", StawkaVAT: "23", Jednostka: "szt"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	p := pozycje[0]
	sprawdzKwote(t, "kwota_netto", p.KwotaNetto, "99.99")
	sprawdzKwote(t, "kwota_vat", p.KwotaVAT, "23.00")
	sprawdzKwote(t, "kwota_brutto", p.KwotaBrutto, "122.99")

	sprawdzKwote(t, "razem_netto", sumy.Netto, "99.99")
	sprawdzKwote(t, "razem_vat", sumy.VAT, "23.00")
	sprawdzKwote(t, "razem_brutto", sumy.Brutto, "122.99")

	// Brutto musi być sumą netto i VAT co do grosza.
	if !sumy.Netto.Add(sumy.VAT).Equal(sumy.Brutto) {
		t.Errorf("netto + VAT = %s, a brutto = %s", sumy.Netto.Add(sumy.VAT), sumy.Brutto)
	}
}

func TestObliczKwotaTrafiajacaDokladnieW0005(t *testing.T) {
	// VAT wypada dokładnie na połówce grosza: 0.50 × 23% = 0.115.
	// Zaokrąglenie half-away-from-zero daje 0.12, nie 0.11.
	pozycje, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Drobiazg", Ilosc: "1", CenaNetto: "0.50", StawkaVAT: "23"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	sprawdzKwote(t, "kwota_netto", pozycje[0].KwotaNetto, "0.50")
	sprawdzKwote(t, "kwota_vat", pozycje[0].KwotaVAT, "0.12")
	sprawdzKwote(t, "kwota_brutto", pozycje[0].KwotaBrutto, "0.62")

	// Drugi wariant trafiający w połówkę: 2.50 × 5% = 0.125 → 0.13.
	pozycje2, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Drobiazg", Ilosc: "1", CenaNetto: "2.50", StawkaVAT: "5"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	sprawdzKwote(t, "kwota_vat", pozycje2[0].KwotaVAT, "0.13")

	// I trzeci: 0.10 × 5% = 0.005 → 0.01.
	pozycje3, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Drobiazg", Ilosc: "1", CenaNetto: "0.10", StawkaVAT: "5"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	sprawdzKwote(t, "kwota_vat", pozycje3[0].KwotaVAT, "0.01")
}

func TestObliczSumujeZaokragloneWartosciPozycji(t *testing.T) {
	// Trzy identyczne pozycje po 33.33: suma VAT-u ma być sumą zaokrąglonych
	// pozycji (3 × 7.67 = 23.01), a nie VAT-em policzonym od sumy (22.9977 → 23.00).
	// Dzięki temu suma na dokumencie zgadza się z tym, co widać w pozycjach.
	pozycje, sumy, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "A", Ilosc: "1", CenaNetto: "33.33", StawkaVAT: "23"},
		{Opis: "B", Ilosc: "1", CenaNetto: "33.33", StawkaVAT: "23"},
		{Opis: "C", Ilosc: "1", CenaNetto: "33.33", StawkaVAT: "23"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	for i, p := range pozycje {
		sprawdzKwote(t, "kwota_vat pozycji", p.KwotaVAT, "7.67")
		if i == 0 {
			sprawdzKwote(t, "kwota_brutto pozycji", p.KwotaBrutto, "41.00")
		}
	}
	sprawdzKwote(t, "razem_netto", sumy.Netto, "99.99")
	sprawdzKwote(t, "razem_vat", sumy.VAT, "23.01")
	sprawdzKwote(t, "razem_brutto", sumy.Brutto, "123.00")

	suma := decimal.Zero
	for _, p := range pozycje {
		suma = suma.Add(p.KwotaBrutto)
	}
	if !suma.Equal(sumy.Brutto) {
		t.Errorf("suma brutto pozycji = %s, a podsumowanie = %s", suma, sumy.Brutto)
	}
}

func TestObliczZestawienieWedlugStawek(t *testing.T) {
	_, sumy, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa 23", Ilosc: "1", CenaNetto: "100", StawkaVAT: "23"},
		{Opis: "Książka 5", Ilosc: "2", CenaNetto: "50", StawkaVAT: "5"},
		{Opis: "Usługa 23 druga", Ilosc: "1", CenaNetto: "100", StawkaVAT: "23"},
		{Opis: "Zwolniona", Ilosc: "1", CenaNetto: "300", StawkaVAT: "zw"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	if len(sumy.WedlugStaw) != 3 {
		t.Fatalf("zestawienie ma %d wierszy, chcę 3", len(sumy.WedlugStaw))
	}
	// Kolejność pierwszego wystąpienia: 23, 5, zw.
	chciane := []struct{ stawka, netto, vat string }{
		{"23", "200.00", "46.00"},
		{"5", "100.00", "5.00"},
		{"zw", "300.00", "0.00"},
	}
	for i, c := range chciane {
		w := sumy.WedlugStaw[i]
		if w.StawkaVAT != c.stawka {
			t.Errorf("wiersz %d: stawka = %q, chcę %q", i, w.StawkaVAT, c.stawka)
		}
		sprawdzKwote(t, "netto wiersza", w.Netto, c.netto)
		sprawdzKwote(t, "vat wiersza", w.VAT, c.vat)
	}
	sprawdzKwote(t, "razem_netto", sumy.Netto, "600.00")
	sprawdzKwote(t, "razem_vat", sumy.VAT, "51.00")
	sprawdzKwote(t, "razem_brutto", sumy.Brutto, "651.00")
}

func TestObliczStawkaZwolnionaDajeZeroVAT(t *testing.T) {
	for _, stawka := range []string{"zw", "ZW", " zw "} {
		pozycje, _, err := Oblicz([]PozycjaWejsciowa{
			{Opis: "Szkolenie", Ilosc: "1", CenaNetto: "1000", StawkaVAT: stawka},
		}, "", stawkiTestowe(t))
		if err != nil {
			t.Fatalf("Oblicz(stawka=%q) = %v", stawka, err)
		}
		sprawdzKwote(t, "kwota_vat", pozycje[0].KwotaVAT, "0.00")
		sprawdzKwote(t, "kwota_brutto", pozycje[0].KwotaBrutto, "1000.00")
		if pozycje[0].StawkaVATID != "5" {
			t.Errorf("ID stawki zw = %q, chcę 5", pozycje[0].StawkaVATID)
		}
	}
}

func TestObliczStawkaBezMapowaniaDajeCzytelnyBlad(t *testing.T) {
	_, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "1", CenaNetto: "100", StawkaVAT: "7"},
	}, "", stawkiTestowe(t))
	if err == nil {
		t.Fatal("Oblicz = nil, chcę błędu dla stawki bez mapowania")
	}
	komunikat := err.Error()
	// Komunikat trafia do modelu, więc musi mówić dokładnie, co zrobić.
	for _, fragment := range []string{"7", "SYSTIM_VAT_IDS", "lista_stawek_vat", "pozycja 1"} {
		if !strings.Contains(komunikat, fragment) {
			t.Errorf("komunikat %q nie zawiera %q", komunikat, fragment)
		}
	}
	// Powinien też wymienić stawki, które są dostępne.
	if !strings.Contains(komunikat, "23 → ID 1") {
		t.Errorf("komunikat %q nie wymienia skonfigurowanych stawek", komunikat)
	}
}

func TestObliczNormalizacjaZapisuStawki(t *testing.T) {
	// "23", "23%", "23,00" i " 23 " to ta sama stawka.
	for _, zapis := range []string{"23", "23%", "23,00", " 23 ", "23.0"} {
		pozycje, _, err := Oblicz([]PozycjaWejsciowa{
			{Opis: "Usługa", Ilosc: "1", CenaNetto: "100", StawkaVAT: zapis},
		}, "", stawkiTestowe(t))
		if err != nil {
			t.Fatalf("Oblicz(stawka=%q) = %v", zapis, err)
		}
		if pozycje[0].StawkaVATID != "1" {
			t.Errorf("stawka %q dała ID %q, chcę 1", zapis, pozycje[0].StawkaVATID)
		}
		sprawdzKwote(t, "kwota_vat", pozycje[0].KwotaVAT, "23.00")
	}
}

func TestObliczRabatObnizaCeneJednostkowa(t *testing.T) {
	// API Systim nie liczy kwot, więc rabat musimy zastosować sami — samo przesłanie
	// pola "rabat" nie obniżyłoby kwot na dokumencie.
	pozycje, sumy, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "2", CenaNetto: "100", StawkaVAT: "23"},
	}, "10", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	sprawdzKwote(t, "cena_netto po rabacie", pozycje[0].CenaNetto, "90.00")
	sprawdzKwote(t, "cena przed rabatem", pozycje[0].CenaPrzedRabat, "100.00")
	sprawdzKwote(t, "kwota_netto", pozycje[0].KwotaNetto, "180.00")
	sprawdzKwote(t, "kwota_vat", pozycje[0].KwotaVAT, "41.40")
	sprawdzKwote(t, "razem_brutto", sumy.Brutto, "221.40")

	// Ilość × cena musi się zgadzać z kwotą netto, inaczej dokument jest wewnętrznie sprzeczny.
	if !pozycje[0].Ilosc.Mul(pozycje[0].CenaNetto).Round(2).Equal(pozycje[0].KwotaNetto) {
		t.Error("ilość × cena jednostkowa ≠ kwota netto")
	}
}

func TestObliczOdrzucaDaneNiepoprawne(t *testing.T) {
	przypadki := []struct {
		nazwa    string
		pozycje  []PozycjaWejsciowa
		rabat    string
		fragment string
	}{
		{"brak pozycji", nil, "", "co najmniej jedną pozycję"},
		{"pusty opis", []PozycjaWejsciowa{{Opis: "  ", Ilosc: "1", CenaNetto: "10", StawkaVAT: "23"}}, "", "opis jest wymagany"},
		{"ilość zero", []PozycjaWejsciowa{{Opis: "A", Ilosc: "0", CenaNetto: "10", StawkaVAT: "23"}}, "", "większa od zera"},
		{"ilość ujemna", []PozycjaWejsciowa{{Opis: "A", Ilosc: "-1", CenaNetto: "10", StawkaVAT: "23"}}, "", "większa od zera"},
		{"cena ujemna", []PozycjaWejsciowa{{Opis: "A", Ilosc: "1", CenaNetto: "-10", StawkaVAT: "23"}}, "", "nie może być ujemna"},
		{"ilość nieliczbowa", []PozycjaWejsciowa{{Opis: "A", Ilosc: "dużo", CenaNetto: "10", StawkaVAT: "23"}}, "", "nie jest liczbą"},
		{"pusta cena", []PozycjaWejsciowa{{Opis: "A", Ilosc: "1", CenaNetto: "", StawkaVAT: "23"}}, "", "wymagana"},
		{"rabat 100%", []PozycjaWejsciowa{{Opis: "A", Ilosc: "1", CenaNetto: "10", StawkaVAT: "23"}}, "100", "poza zakresem"},
		{"rabat ujemny", []PozycjaWejsciowa{{Opis: "A", Ilosc: "1", CenaNetto: "10", StawkaVAT: "23"}}, "-5", "poza zakresem"},
	}
	for _, p := range przypadki {
		t.Run(p.nazwa, func(t *testing.T) {
			_, _, err := Oblicz(p.pozycje, p.rabat, stawkiTestowe(t))
			if err == nil {
				t.Fatalf("Oblicz = nil, chcę błędu")
			}
			if !strings.Contains(err.Error(), p.fragment) {
				t.Errorf("err = %v, chcę wzmianki o %q", err, p.fragment)
			}
		})
	}
}

func TestObliczPrzecinekJakoSeparatorDziesietny(t *testing.T) {
	pozycje, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "1,5", CenaNetto: "199,99", StawkaVAT: "23"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	// 1.5 × 199.99 = 299.985 → 299.99
	sprawdzKwote(t, "kwota_netto", pozycje[0].KwotaNetto, "299.99")
}

func TestNoweStawkiVATWalidacja(t *testing.T) {
	if _, err := NoweStawkiVAT(nil); err == nil {
		t.Error("puste mapowanie zostało przyjęte")
	}
	if _, err := NoweStawkiVAT(map[string]int{"23": 0}); err == nil {
		t.Error("ID stawki równe 0 zostało przyjęte")
	}
	if _, err := NoweStawkiVAT(map[string]int{"23": 1, "23%": 2}); err == nil {
		t.Error("duplikat stawki po normalizacji został przyjęty")
	}
}

func TestNaPozycjeSystimPrzenosiIDStawkiNieProcent(t *testing.T) {
	pozycje, _, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Usługa", Ilosc: "1", CenaNetto: "100", StawkaVAT: "23", IDProduktu: "88", Jednostka: "h"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	sys := NaPozycjeSystim(pozycje)
	if len(sys) != 1 {
		t.Fatalf("dostałem %d pozycji", len(sys))
	}
	// Do API idzie ID stawki (1), a nie procent (23) — to jest ta pułapka.
	if sys[0].StawkaVatID != "1" {
		t.Errorf("StawkaVatID = %q, chcę 1 (ID stawki, nie procent)", sys[0].StawkaVatID)
	}
	if sys[0].IDProduktu != "88" || sys[0].Jednostka != "h" {
		t.Errorf("pozycja = %+v", sys[0])
	}
}

func sprawdzKwote(t *testing.T, opis string, got decimal.Decimal, chce string) {
	t.Helper()
	if got.StringFixed(2) != chce {
		t.Errorf("%s = %s, chcę %s", opis, got.StringFixed(2), chce)
	}
}
