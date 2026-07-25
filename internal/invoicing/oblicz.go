package invoicing

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/Mariuszkru/mcpsystim/internal/systim"
)

// PozycjaWejsciowa to pozycja tak, jak podaje ją użytkownik: bez policzonych kwot.
//
// Ilość i cena przychodzą jako stringi, żeby nigdzie po drodze nie pojawił się
// float64 — parsujemy je wprost do decimal.Decimal.
type PozycjaWejsciowa struct {
	IDProduktu string
	Opis       string
	Ilosc      string
	Jednostka  string
	CenaNetto  string
	StawkaVAT  string
}

// PozycjaObliczona to pozycja z kwotami policzonymi po naszej stronie.
type PozycjaObliczona struct {
	IDProduktu     string          `json:"id_produktu,omitempty"`
	Opis           string          `json:"opis"`
	Ilosc          decimal.Decimal `json:"ilosc"`
	Jednostka      string          `json:"jednostka,omitempty"`
	CenaNetto      decimal.Decimal `json:"cena_netto"`
	KwotaNetto     decimal.Decimal `json:"kwota_netto"`
	StawkaVAT      string          `json:"stawka_vat"`
	StawkaVATID    string          `json:"stawka_vat_id"`
	ProcentVAT     decimal.Decimal `json:"procent_vat"`
	KwotaVAT       decimal.Decimal `json:"kwota_vat"`
	KwotaBrutto    decimal.Decimal `json:"kwota_brutto"`
	CenaPrzedRabat decimal.Decimal `json:"cena_przed_rabatem,omitempty"`
}

// WierszVAT to jedna linia zestawienia stawek na dokumencie.
type WierszVAT struct {
	StawkaVAT string          `json:"stawka_vat"`
	Netto     decimal.Decimal `json:"netto"`
	VAT       decimal.Decimal `json:"vat"`
	Brutto    decimal.Decimal `json:"brutto"`
}

// Podsumowanie to sumy dokumentu.
type Podsumowanie struct {
	Netto      decimal.Decimal `json:"razem_netto"`
	VAT        decimal.Decimal `json:"razem_vat"`
	Brutto     decimal.Decimal `json:"razem_brutto"`
	WedlugStaw []WierszVAT     `json:"wedlug_stawek"`
}

// Oblicz przelicza pozycje dokumentu.
//
// API Systim nie liczy kwot — to, co wyślemy, ląduje na dokumencie dosłownie.
// Dlatego wszystko liczymy tutaj, na decimal.Decimal, zaokrąglając do dwóch miejsc
// metodą half-away-from-zero (0.005 → 0.01), co odpowiada polskiej praktyce
// zaokrąglania kwot na fakturach.
//
// rabatProcent to rabat na całym dokumencie, podany jako procent bez znaku %.
// Jest stosowany do ceny jednostkowej każdej pozycji, bo API nie przeliczy go za nas.
func Oblicz(pozycje []PozycjaWejsciowa, rabatProcent string, stawki *StawkiVAT) ([]PozycjaObliczona, Podsumowanie, error) {
	if len(pozycje) == 0 {
		return nil, Podsumowanie{}, fmt.Errorf("dokument musi mieć co najmniej jedną pozycję")
	}

	rabat, err := parsujRabat(rabatProcent)
	if err != nil {
		return nil, Podsumowanie{}, err
	}
	// Mnożnik ceny po rabacie, np. dla rabatu 5% to 0.95.
	mnoznik := decimal.NewFromInt(100).Sub(rabat).Div(decimal.NewFromInt(100))

	obliczone := make([]PozycjaObliczona, 0, len(pozycje))
	for i, p := range pozycje {
		nr := i + 1
		if strings.TrimSpace(p.Opis) == "" {
			return nil, Podsumowanie{}, fmt.Errorf("pozycja %d: opis jest wymagany", nr)
		}
		ilosc, err := parsujLiczbe(p.Ilosc, fmt.Sprintf("pozycja %d: ilość", nr))
		if err != nil {
			return nil, Podsumowanie{}, err
		}
		if !ilosc.IsPositive() {
			return nil, Podsumowanie{}, fmt.Errorf("pozycja %d: ilość musi być większa od zera, dostałem %s", nr, ilosc)
		}
		cena, err := parsujLiczbe(p.CenaNetto, fmt.Sprintf("pozycja %d: cena netto", nr))
		if err != nil {
			return nil, Podsumowanie{}, err
		}
		if cena.IsNegative() {
			return nil, Podsumowanie{}, fmt.Errorf("pozycja %d: cena netto nie może być ujemna", nr)
		}

		idStawki, err := stawki.ID(p.StawkaVAT)
		if err != nil {
			return nil, Podsumowanie{}, fmt.Errorf("pozycja %d: %w", nr, err)
		}
		procent, err := stawki.Procent(p.StawkaVAT)
		if err != nil {
			return nil, Podsumowanie{}, fmt.Errorf("pozycja %d: %w", nr, err)
		}

		cenaPoRabacie := cena
		if rabat.IsPositive() {
			cenaPoRabacie = cena.Mul(mnoznik).Round(2)
		}

		kwotaNetto := ilosc.Mul(cenaPoRabacie).Round(2)
		kwotaVAT := kwotaNetto.Mul(procent).Div(decimal.NewFromInt(100)).Round(2)
		kwotaBrutto := kwotaNetto.Add(kwotaVAT)

		o := PozycjaObliczona{
			IDProduktu:  strings.TrimSpace(p.IDProduktu),
			Opis:        strings.TrimSpace(p.Opis),
			Ilosc:       ilosc,
			Jednostka:   strings.TrimSpace(p.Jednostka),
			CenaNetto:   cenaPoRabacie,
			KwotaNetto:  kwotaNetto,
			StawkaVAT:   NormalizujStawke(p.StawkaVAT),
			StawkaVATID: idStawki,
			ProcentVAT:  procent,
			KwotaVAT:    kwotaVAT,
			KwotaBrutto: kwotaBrutto,
		}
		if rabat.IsPositive() {
			o.CenaPrzedRabat = cena
		}
		obliczone = append(obliczone, o)
	}

	return obliczone, podsumuj(obliczone), nil
}

// podsumuj sumuje kwoty pozycji. Sumujemy wartości już zaokrąglone, żeby suma
// na dokumencie zgadzała się z sumą widocznych pozycji — księgowo to właściwa
// kolejność działań.
func podsumuj(pozycje []PozycjaObliczona) Podsumowanie {
	p := Podsumowanie{Netto: decimal.Zero, VAT: decimal.Zero, Brutto: decimal.Zero}

	// Zestawienie według stawek, w kolejności pierwszego wystąpienia.
	kolejnosc := make([]string, 0, 4)
	wg := make(map[string]*WierszVAT, 4)

	for _, poz := range pozycje {
		p.Netto = p.Netto.Add(poz.KwotaNetto)
		p.VAT = p.VAT.Add(poz.KwotaVAT)
		p.Brutto = p.Brutto.Add(poz.KwotaBrutto)

		w, ok := wg[poz.StawkaVAT]
		if !ok {
			w = &WierszVAT{StawkaVAT: poz.StawkaVAT, Netto: decimal.Zero, VAT: decimal.Zero, Brutto: decimal.Zero}
			wg[poz.StawkaVAT] = w
			kolejnosc = append(kolejnosc, poz.StawkaVAT)
		}
		w.Netto = w.Netto.Add(poz.KwotaNetto)
		w.VAT = w.VAT.Add(poz.KwotaVAT)
		w.Brutto = w.Brutto.Add(poz.KwotaBrutto)
	}

	p.WedlugStaw = make([]WierszVAT, 0, len(kolejnosc))
	for _, s := range kolejnosc {
		p.WedlugStaw = append(p.WedlugStaw, *wg[s])
	}
	return p
}

// NaPozycjeSystim przekłada policzone pozycje na strukturę wysyłaną do API.
func NaPozycjeSystim(pozycje []PozycjaObliczona) []systim.PozycjaFaktury {
	out := make([]systim.PozycjaFaktury, 0, len(pozycje))
	for _, p := range pozycje {
		out = append(out, systim.PozycjaFaktury{
			IDProduktu:  p.IDProduktu,
			Opis:        p.Opis,
			Ilosc:       p.Ilosc,
			Jednostka:   p.Jednostka,
			CenaNetto:   p.CenaNetto,
			KwotaNetto:  p.KwotaNetto,
			StawkaVatID: p.StawkaVATID,
			KwotaVat:    p.KwotaVAT,
			KwotaBrutto: p.KwotaBrutto,
		})
	}
	return out
}

// parsujLiczbe odczytuje liczbę dziesiętną, akceptując przecinek jako separator.
func parsujLiczbe(s, opis string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, fmt.Errorf("%s: wartość jest wymagana", opis)
	}
	// Spacje jako separator tysięcy zdarzają się przy kopiowaniu z arkusza.
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s: %q nie jest liczbą (użyj kropki dziesiętnej, np. 33.33)", opis, s)
	}
	return d, nil
}

// parsujRabat odczytuje rabat dokumentu podany jako procent bez znaku %.
func parsujRabat(s string) (decimal.Decimal, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, nil
	}
	s = strings.TrimSuffix(s, "%")
	d, err := parsujLiczbe(s, "rabat")
	if err != nil {
		return decimal.Zero, err
	}
	if d.IsNegative() || d.GreaterThanOrEqual(decimal.NewFromInt(100)) {
		return decimal.Zero, fmt.Errorf("rabat %s%% jest poza zakresem — oczekuję wartości od 0 do poniżej 100", d)
	}
	return d, nil
}
