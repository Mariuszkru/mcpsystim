package systim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Rodzaje dokumentów sprzedaży — wartość pola "rodzaj" w addSellInvoice.
const (
	RodzajFakturaVAT      = 0
	RodzajProForma        = 1
	RodzajParagonFiskalny = 6
	RodzajParagonNiefisk  = 15
	RodzajRachunek        = 22
	RodzajFakturaWWalucie = 23
	RodzajOferta          = 26
)

// nazwyRodzajow opisuje obsługiwane rodzaje dokumentów po polsku.
var nazwyRodzajow = map[int]string{
	RodzajFakturaVAT:      "faktura VAT",
	RodzajProForma:        "faktura pro forma",
	RodzajParagonFiskalny: "paragon fiskalny",
	RodzajParagonNiefisk:  "paragon niefiskalny",
	RodzajRachunek:        "rachunek",
	RodzajFakturaWWalucie: "faktura w walucie",
	RodzajOferta:          "oferta",
}

// rodzajeWalutowe wymagają dodatkowo pól waluta, data_waluty, kurs_waluty
// i platnosc_walutowa. Nie są zaimplementowane i są jawnie odrzucane.
var rodzajeWalutowe = map[int]bool{23: true, 25: true, 29: true, 43: true, 44: true}

// NazwaRodzaju zwraca polską nazwę rodzaju dokumentu.
func NazwaRodzaju(r int) string {
	if n, ok := nazwyRodzajow[r]; ok {
		return n
	}
	return fmt.Sprintf("rodzaj %d", r)
}

// ObslugiwaneRodzaje zwraca posortowaną listę rodzajów, które można wystawić.
func ObslugiwaneRodzaje() []int {
	out := make([]int, 0, len(nazwyRodzajow))
	for r := range nazwyRodzajow {
		if rodzajeWalutowe[r] {
			continue
		}
		out = append(out, r)
	}
	sort.Ints(out)
	return out
}

// SprawdzRodzaj waliduje rodzaj dokumentu i odrzuca warianty walutowe.
func SprawdzRodzaj(r int) error {
	if rodzajeWalutowe[r] {
		return fmt.Errorf(
			"rodzaj %d wymaga waluty obcej (pola waluta, data_waluty, kurs_waluty, platnosc_walutowa), "+
				"a obsługa walut obcych nie jest w tym serwerze zaimplementowana. "+
				"Wystaw dokument w PLN albo dodaj go ręcznie w panelu Systim", r)
	}
	if _, ok := nazwyRodzajow[r]; !ok {
		return fmt.Errorf("nieznany rodzaj dokumentu %d; dozwolone: %s", r, opisRodzajow())
	}
	return nil
}

func opisRodzajow() string {
	czesci := make([]string, 0, len(nazwyRodzajow))
	for _, r := range ObslugiwaneRodzaje() {
		czesci = append(czesci, fmt.Sprintf("%d = %s", r, nazwyRodzajow[r]))
	}
	return strings.Join(czesci, ", ")
}

// FormyPlatnosci to wartości akceptowane przez pole forma_platnosci.
var FormyPlatnosci = []string{
	"przelew", "gotówka", "barter", "za pobraniem", "rozliczenie saldami", "karta płatnicza",
}

// PozycjaFaktury to jedna linia dokumentu z policzonymi już kwotami.
//
// API Systim nie liczy kwot — wszystko, co tu trafi, ląduje na dokumencie dosłownie.
// Wyliczeniem zajmuje się pakiet invoicing.
type PozycjaFaktury struct {
	IDProduktu  string
	Opis        string
	Ilosc       decimal.Decimal
	Jednostka   string
	CenaNetto   decimal.Decimal
	KwotaNetto  decimal.Decimal
	StawkaVatID string // ID stawki w Systim, nie procent
	KwotaVat    decimal.Decimal
	KwotaBrutto decimal.Decimal
}

// ZadanieFaktury to komplet danych do wystawienia dokumentu sprzedaży.
type ZadanieFaktury struct {
	IDKontrahenta   string
	DataWystawienia string // RRRR-MM-DD
	DataSprzedazy   string // RRRR-MM-DD
	Rodzaj          int
	IDSzablonu      string
	IDNumeracji     string
	Pozycje         []PozycjaFaktury

	TerminPlatnosci int // w dniach; 0 = nie wysyłamy pola
	FormaPlatnosci  string
	Uwagi           string
	// Rabat to procent bez znaku %. UWAGA: NIE jest wysyłany do API — patrz
	// komentarz przy BudujParametryFaktury. Kwoty są już po rabacie.
	Rabat        string
	WyslijEmail  bool
	EmailAdres   string
	WyslijDoKSeF bool
}

// Kody zwracane w result_code metody addSellInvoice.
const (
	ResultKsiegowanieWylaczone = 0
	ResultZapisUtworzony       = 100
	ResultZapisZaktualizowany  = 101
	ResultKsiegowanieNieudane  = 102
)

// WynikFaktury to odpowiedź Systim po wystawieniu dokumentu.
type WynikFaktury struct {
	ID         string
	Numer      string
	ResultCode int
	KsefData   string
}

// odpowiedzFaktury to surowy kształt result metody addSellInvoice.
type odpowiedzFaktury struct {
	ID         HTMLString `json:"id"`
	Numer      HTMLString `json:"numer"`
	ResultCode FlexInt    `json:"result_code"`
	KsefData   HTMLString `json:"ksef_data"`
}

// OpisResultCode tłumaczy result_code na komunikat dla użytkownika. Drugi zwracany
// parametr mówi, czy sytuacja wymaga uwagi użytkownika.
func OpisResultCode(kod int) (string, bool) {
	switch kod {
	case ResultKsiegowanieWylaczone:
		return "Księgowanie automatyczne jest wyłączone — dokument powstał, ale nie utworzono zapisu w księgowości.", false
	case ResultZapisUtworzony:
		return "Utworzono zapis w księgowości.", false
	case ResultZapisZaktualizowany:
		return "Zaktualizowano istniejący zapis w księgowości.", false
	case ResultKsiegowanieNieudane:
		return "UWAGA: dokument został wystawiony, ale księgowanie się NIE powiodło (result_code 102). " +
			"Faktura istnieje i ma numer, natomiast zapis w księgowości nie powstał — trzeba to " +
			"sprawdzić i poprawić ręcznie w panelu Systim.", true
	default:
		return fmt.Sprintf("Systim zwróciło nieznany result_code %d.", kod), true
	}
}

// AddSellInvoice wystawia dokument sprzedaży.
//
// Operacja jest nieodwracalna — powstaje dokument księgowy z nadanym numerem.
func (c *Client) AddSellInvoice(ctx context.Context, z ZadanieFaktury) (WynikFaktury, error) {
	params, err := BudujParametryFaktury(z)
	if err != nil {
		return WynikFaktury{}, err
	}
	raw, err := c.Wywolaj(ctx, "addSellInvoice", params)
	if err != nil {
		return WynikFaktury{}, err
	}
	var o odpowiedzFaktury
	if err := json.Unmarshal(raw, &o); err != nil {
		return WynikFaktury{}, fmt.Errorf("wystawienie dokumentu: nie umiem odczytać odpowiedzi: %w", err)
	}
	return WynikFaktury{
		ID:         o.ID.String(),
		Numer:      o.Numer.String(),
		ResultCode: o.ResultCode.Int(),
		KsefData:   o.KsefData.String(),
	}, nil
}

// BudujParametryFaktury składa ciało form-urlencoded dla addSellInvoice.
//
// Pole "rabat" celowo NIE jest wysyłane, mimo że API je przyjmuje. Powód jest
// empiryczny: przy dokumencie z rabatem i trzema lub więcej pozycjami backend
// Systim przewraca się błędem PHP „Cannot assign an empty string to a string
// offset" i nie wystawia dokumentu. Z dwiema pozycjami przechodzi, z trzema —
// powtarzalnie nie. Potwierdzone na żywym koncie.
//
// Nic na tym nie tracimy: API i tak nie liczy kwot, więc rabat jest już
// uwzględniony w cenach jednostkowych i kwotach wyliczonych przez pakiet
// invoicing. Przesłanie tego pola miało wyłącznie znaczenie informacyjne na
// wydruku, a przy okazji groziło pokazaniem rabatu dwukrotnie.
//
// Pozycje idą jako równoległe tablice w konwencji PHP: opis[0], ilosc[0], ...,
// opis[1], ... Klucze ustawiamy dosłownie z nawiasami; url.Values zakoduje je
// procentowo, a PHP rozkoduje z powrotem do tablic.
func BudujParametryFaktury(z ZadanieFaktury) (url.Values, error) {
	if err := SprawdzRodzaj(z.Rodzaj); err != nil {
		return nil, err
	}
	if strings.TrimSpace(z.IDKontrahenta) == "" {
		return nil, fmt.Errorf("pole id_kontrahenta jest wymagane")
	}
	if strings.TrimSpace(z.DataWystawienia) == "" {
		return nil, fmt.Errorf("pole data_wystawienia jest wymagane")
	}
	if strings.TrimSpace(z.DataSprzedazy) == "" {
		return nil, fmt.Errorf("pole data_sprzedazy jest wymagane")
	}
	// Brak któregokolwiek z tych dwóch pól powoduje odrzucenie dokumentu przez API.
	if strings.TrimSpace(z.IDSzablonu) == "" {
		return nil, fmt.Errorf("pole id_szablonu jest wymagane (konfiguracja SYSTIM_ID_SZABLONU)")
	}
	if strings.TrimSpace(z.IDNumeracji) == "" {
		return nil, fmt.Errorf("pole id_numeracji jest wymagane (konfiguracja SYSTIM_ID_NUMERACJI)")
	}
	if len(z.Pozycje) == 0 {
		return nil, fmt.Errorf("dokument musi mieć co najmniej jedną pozycję")
	}

	v := url.Values{}
	v.Set("id_kontrahenta", z.IDKontrahenta)
	v.Set("data_wystawienia", z.DataWystawienia)
	v.Set("data_sprzedazy", z.DataSprzedazy)
	v.Set("rodzaj", strconv.Itoa(z.Rodzaj))
	v.Set("id_szablonu", z.IDSzablonu)
	v.Set("id_numeracji", z.IDNumeracji)

	if z.TerminPlatnosci > 0 {
		v.Set("termin_platnosci", strconv.Itoa(z.TerminPlatnosci))
	}
	if z.FormaPlatnosci != "" {
		v.Set("forma_platnosci", z.FormaPlatnosci)
	}
	if z.Uwagi != "" {
		v.Set("uwagi", z.Uwagi)
	}
	if z.WyslijEmail {
		if strings.TrimSpace(z.EmailAdres) == "" {
			return nil, fmt.Errorf("wysyłka e-mailem wymaga podania adresu e-mail")
		}
		v.Set("email_wyslij", "1")
		v.Set("email_adres", z.EmailAdres)
	}
	if z.WyslijDoKSeF {
		v.Set("wyslij_do_ksef", "1")
	}

	for i, p := range z.Pozycje {
		if strings.TrimSpace(p.StawkaVatID) == "" {
			return nil, fmt.Errorf("pozycja %d: brak ID stawki VAT", i+1)
		}
		if p.IDProduktu != "" {
			v.Set(fmt.Sprintf("id_produktu[%d]", i), p.IDProduktu)
		}
		v.Set(fmt.Sprintf("opis[%d]", i), p.Opis)
		v.Set(fmt.Sprintf("ilosc[%d]", i), p.Ilosc.String())
		if p.Jednostka != "" {
			v.Set(fmt.Sprintf("jednostka[%d]", i), p.Jednostka)
		}
		v.Set(fmt.Sprintf("cena_netto[%d]", i), p.CenaNetto.String())
		v.Set(fmt.Sprintf("kwota_netto[%d]", i), p.KwotaNetto.StringFixed(2))
		v.Set(fmt.Sprintf("stawka_vat[%d]", i), p.StawkaVatID)
		v.Set(fmt.Sprintf("kwota_vat[%d]", i), p.KwotaVat.StringFixed(2))
		v.Set(fmt.Sprintf("kwota_brutto[%d]", i), p.KwotaBrutto.StringFixed(2))
	}

	return v, nil
}
