// Package invoicing liczy kwoty dokumentu i obsługuje szkice faktur.
//
// Wszystkie kwoty są liczone na decimal.Decimal — nigdy na float64. Dokument
// księgowy musi się zgadzać co do grosza, a float64 nie potrafi dokładnie
// reprezentować nawet 0.1.
package invoicing

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// StawkiVAT mapuje stawkę podaną przez użytkownika ("23", "zw") na ID stawki
// w Systim. Pole stawka_vat w API przyjmuje ID, nie procent — to jedna z częstszych
// pomyłek przy tej integracji.
type StawkiVAT struct {
	// poID trzyma znormalizowany klucz → ID stawki w Systim.
	poID map[string]string
}

// NoweStawkiVAT buduje mapowanie z konfiguracji SYSTIM_VAT_IDS.
//
// Klucze to procenty bez znaku % ("23", "8", "5", "0") albo oznaczenia stawek
// nieprocentowych ("zw", "np", "oo"). Wartości to ID stawek w Systim.
func NoweStawkiVAT(m map[string]int) (*StawkiVAT, error) {
	if len(m) == 0 {
		return nil, fmt.Errorf("mapowanie stawek VAT jest puste — uzupełnij SYSTIM_VAT_IDS " +
			"(ID stawek odczytasz narzędziem lista_stawek_vat)")
	}
	s := &StawkiVAT{poID: make(map[string]string, len(m))}
	for k, id := range m {
		klucz := NormalizujStawke(k)
		if klucz == "" {
			return nil, fmt.Errorf("SYSTIM_VAT_IDS zawiera pusty klucz stawki")
		}
		if id <= 0 {
			return nil, fmt.Errorf("SYSTIM_VAT_IDS: stawka %q ma nieprawidłowe ID %d (oczekuję liczby dodatniej)", k, id)
		}
		if _, ok := s.poID[klucz]; ok {
			return nil, fmt.Errorf("SYSTIM_VAT_IDS: stawka %q występuje dwukrotnie po normalizacji do %q", k, klucz)
		}
		s.poID[klucz] = fmt.Sprint(id)
	}
	return s, nil
}

// NormalizujStawke sprowadza zapis stawki do postaci porównywalnej: małe litery,
// bez znaku procenta, bez spacji, bez zbędnych zer po przecinku ("23,00 %" → "23").
func NormalizujStawke(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "%", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, ",", ".")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// "23.00" i "23" to ta sama stawka.
	if d, err := decimal.NewFromString(s); err == nil {
		return d.String()
	}
	return s
}

// stawkiBezProcentu to oznaczenia stawek, przy których VAT wynosi zero.
var stawkiBezProcentu = map[string]bool{
	"zw": true, // zwolniony
	"np": true, // nie podlega
	"oo": true, // odwrotne obciążenie
}

// ID zwraca ID stawki w Systim dla zapisu podanego przez użytkownika.
func (s *StawkiVAT) ID(stawka string) (string, error) {
	klucz := NormalizujStawke(stawka)
	if klucz == "" {
		return "", fmt.Errorf("nie podano stawki VAT")
	}
	id, ok := s.poID[klucz]
	if !ok {
		return "", fmt.Errorf(
			"stawka VAT %q nie ma mapowania na ID w Systim. Skonfigurowane stawki: %s. "+
				"Odczytaj ID stawek narzędziem lista_stawek_vat i uzupełnij zmienną środowiskową "+
				"SYSTIM_VAT_IDS, np. {\"23\":1,\"8\":2,\"5\":3,\"0\":4,\"zw\":5}",
			stawka, s.opisDostepnych())
	}
	return id, nil
}

// Procent zwraca wartość procentową stawki do wyliczenia kwoty VAT.
// Stawki nieprocentowe (zw, np, oo) dają zero.
func (s *StawkiVAT) Procent(stawka string) (decimal.Decimal, error) {
	klucz := NormalizujStawke(stawka)
	if stawkiBezProcentu[klucz] {
		return decimal.Zero, nil
	}
	p, err := decimal.NewFromString(klucz)
	if err != nil {
		return decimal.Zero, fmt.Errorf(
			"nie umiem odczytać stawki VAT %q jako procentu; oczekuję liczby (np. 23) "+
				"albo oznaczenia zw/np/oo", stawka)
	}
	if p.IsNegative() || p.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, fmt.Errorf("stawka VAT %q jest poza zakresem 0–100", stawka)
	}
	return p, nil
}

// Dostepne zwraca posortowaną listę skonfigurowanych stawek.
func (s *StawkiVAT) Dostepne() []string {
	out := make([]string, 0, len(s.poID))
	for k := range s.poID {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (s *StawkiVAT) opisDostepnych() string {
	czesci := make([]string, 0, len(s.poID))
	for _, k := range s.Dostepne() {
		czesci = append(czesci, fmt.Sprintf("%s → ID %s", k, s.poID[k]))
	}
	return strings.Join(czesci, ", ")
}
