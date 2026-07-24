package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mkrukowski/systim-mcp/internal/systim"
)

// --- lista_stawek_vat ---

// WejscieStawki nie ma parametrów, ale MCP wymaga obiektu jako schematu wejścia.
type WejscieStawki struct{}

// StawkaVAT to jedna stawka z kartoteki Systim.
type StawkaVAT struct {
	ID   string            `json:"id" jsonschema:"ID stawki w Systim — tę wartość wpisuje się do SYSTIM_VAT_IDS i do pola stawka_vat"`
	Opis string            `json:"opis" jsonschema:"Opis stawki tak, jak nazywa ją Systim, np. 23% albo zw"`
	Pola map[string]string `json:"pola" jsonschema:"Wszystkie pola rekordu zwrócone przez API, na wypadek gdyby nazwa stawki była w innym polu"`
}

// WyjscieStawki to odpowiedź narzędzia lista_stawek_vat.
type WyjscieStawki struct {
	Stawki         []StawkaVAT `json:"stawki" jsonschema:"Stawki VAT dostępne na koncie Systim"`
	Skonfigurowane []string    `json:"skonfigurowane" jsonschema:"Stawki obecnie zmapowane w zmiennej SYSTIM_VAT_IDS"`
	Wskazowka      string      `json:"wskazowka" jsonschema:"Co zrobić z odczytanymi ID"`
}

func (s *Serwer) listaStawekVAT(ctx context.Context, _ *mcp.CallToolRequest, _ WejscieStawki) (*mcp.CallToolResult, WyjscieStawki, error) {
	rekordy, err := s.klient.ListVatRates(ctx)
	if err != nil {
		return nil, WyjscieStawki{}, bladDlaModelu("odczyt stawek VAT", err)
	}

	stawki := make([]StawkaVAT, 0, len(rekordy))
	for _, r := range rekordy {
		stawki = append(stawki, StawkaVAT{
			ID:   r.ID,
			Opis: r.Pole("nazwa", "stawka", "wartosc", "opis", "symbol"),
			Pola: r.Pola,
		})
	}

	wy := WyjscieStawki{
		Stawki:         stawki,
		Skonfigurowane: s.stawki.Dostepne(),
		Wskazowka: "Przepisz ID stawek do zmiennej środowiskowej SYSTIM_VAT_IDS jako mapę " +
			`procent → ID, np. {"23":1,"8":2,"5":3,"0":4,"zw":5}, i zrestartuj serwer. ` +
			"Dopóki stawka nie ma mapowania, nie da się jej użyć w pozycji faktury.",
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Stawki VAT na koncie Systim (%d):\n", len(stawki))
	for _, st := range stawki {
		fmt.Fprintf(&b, "  ID %s — %s\n", st.ID, st.Opis)
	}
	fmt.Fprintf(&b, "\nObecnie zmapowane w SYSTIM_VAT_IDS: %s\n", strings.Join(wy.Skonfigurowane, ", "))
	b.WriteString(wy.Wskazowka)

	return tekst(b.String()), wy, nil
}

// --- szukaj_kontrahenta ---

// WejscieKontrahent to parametry wyszukiwania kontrahenta.
type WejscieKontrahent struct {
	Fraza string `json:"fraza" jsonschema:"Fragment nazwy firmy albo numer NIP. Wielkość liter nie ma znaczenia, a myślniki i spacje w NIP-ie są ignorowane — 123-456-78-90 znajdzie tak samo jak 1234567890"`
}

// Kontrahent to rekord kartoteki kontrahentów.
type Kontrahent struct {
	ID    string `json:"id_kontrahenta" jsonschema:"ID kontrahenta w Systim — tę wartość podaje się w przygotuj_fakture"`
	Nazwa string `json:"nazwa" jsonschema:"Nazwa kontrahenta"`
	NIP   string `json:"nip" jsonschema:"Numer NIP, jeśli jest wypełniony"`
	Adres string `json:"adres,omitempty" jsonschema:"Adres kontrahenta, jeśli jest wypełniony"`
	Email string `json:"email,omitempty" jsonschema:"Adres e-mail kontrahenta, przydatny przy wysyłce faktury"`
}

// WyjscieKontrahenci to odpowiedź narzędzia szukaj_kontrahenta.
type WyjscieKontrahenci struct {
	Kontrahenci   []Kontrahent `json:"kontrahenci" jsonschema:"Znalezieni kontrahenci"`
	Znaleziono    int          `json:"znaleziono" jsonschema:"Liczba dopasowań przed przycięciem do limitu"`
	Przyciete     bool         `json:"przyciete" jsonschema:"Czy lista została przycięta do limitu 25 wyników"`
	LacznieWBazie int          `json:"lacznie_w_bazie" jsonschema:"Liczba kontrahentów w całej kartotece"`
}

func (s *Serwer) szukajKontrahenta(ctx context.Context, _ *mcp.CallToolRequest, we WejscieKontrahent) (*mcp.CallToolResult, WyjscieKontrahenci, error) {
	fraza := strings.TrimSpace(we.Fraza)
	if fraza == "" {
		return nil, WyjscieKontrahenci{}, fmt.Errorf("podaj frazę do wyszukania — fragment nazwy albo NIP")
	}

	// API Systim nie przyjmuje parametru wyszukiwania, więc filtrujemy po naszej stronie.
	rekordy, err := s.klient.ListCompanies(ctx)
	if err != nil {
		return nil, WyjscieKontrahenci{}, bladDlaModelu("odczyt kartoteki kontrahentów", err)
	}

	dopasowane := filtruj(rekordy, fraza)
	wy := WyjscieKontrahenci{
		Znaleziono:    len(dopasowane),
		LacznieWBazie: len(rekordy),
	}
	if len(dopasowane) > MaxWynikowWyszukiwania {
		dopasowane = dopasowane[:MaxWynikowWyszukiwania]
		wy.Przyciete = true
	}
	for _, r := range dopasowane {
		wy.Kontrahenci = append(wy.Kontrahenci, Kontrahent{
			ID:    r.ID,
			Nazwa: r.Nazwa(),
			NIP:   r.NIP(),
			Adres: sklejAdres(r),
			Email: r.Pole("email", "e-mail", "mail"),
		})
	}

	if len(wy.Kontrahenci) == 0 {
		return tekst(fmt.Sprintf(
			"Nie znaleziono kontrahenta pasującego do %q (przeszukano %d rekordów kartoteki). "+
				"Sprawdź pisownię albo spróbuj krótszego fragmentu nazwy. "+
				"Ten serwer nie potrafi dodać nowego kontrahenta — trzeba go założyć w panelu Systim.",
			fraza, len(rekordy))), wy, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Znaleziono %d kontrahentów pasujących do %q", wy.Znaleziono, fraza)
	if wy.Przyciete {
		fmt.Fprintf(&b, " (pokazuję pierwszych %d)", MaxWynikowWyszukiwania)
	}
	b.WriteString(":\n")
	for _, k := range wy.Kontrahenci {
		fmt.Fprintf(&b, "  id_kontrahenta=%s — %s", k.ID, k.Nazwa)
		if k.NIP != "" {
			fmt.Fprintf(&b, ", NIP %s", k.NIP)
		}
		if k.Adres != "" {
			fmt.Fprintf(&b, ", %s", k.Adres)
		}
		b.WriteByte('\n')
	}
	return tekst(b.String()), wy, nil
}

// --- szukaj_produktu ---

// WejscieProdukt to parametry wyszukiwania produktu.
type WejscieProdukt struct {
	Fraza string `json:"fraza" jsonschema:"Fragment nazwy, kodu albo opisu produktu lub usługi. Wielkość liter nie ma znaczenia"`
}

// Produkt to rekord kartoteki produktów.
type Produkt struct {
	ID        string `json:"id_produktu" jsonschema:"ID produktu w Systim — można je podać w pozycji faktury"`
	Nazwa     string `json:"nazwa" jsonschema:"Nazwa produktu lub usługi"`
	CenaNetto string `json:"cena_netto,omitempty" jsonschema:"Cena netto z kartoteki, jeśli jest ustawiona"`
	Jednostka string `json:"jednostka,omitempty" jsonschema:"Jednostka miary, np. szt, h, kg"`
	StawkaVAT string `json:"stawka_vat,omitempty" jsonschema:"Stawka VAT z kartoteki tak, jak zapisał ją Systim"`
}

// WyjscieProdukty to odpowiedź narzędzia szukaj_produktu.
type WyjscieProdukty struct {
	Produkty      []Produkt `json:"produkty" jsonschema:"Znalezione produkty i usługi"`
	Znaleziono    int       `json:"znaleziono" jsonschema:"Liczba dopasowań przed przycięciem do limitu"`
	Przyciete     bool      `json:"przyciete" jsonschema:"Czy lista została przycięta do limitu 25 wyników"`
	LacznieWBazie int       `json:"lacznie_w_bazie" jsonschema:"Liczba pozycji w całej kartotece produktów"`
}

func (s *Serwer) szukajProduktu(ctx context.Context, _ *mcp.CallToolRequest, we WejscieProdukt) (*mcp.CallToolResult, WyjscieProdukty, error) {
	fraza := strings.TrimSpace(we.Fraza)
	if fraza == "" {
		return nil, WyjscieProdukty{}, fmt.Errorf("podaj frazę do wyszukania — fragment nazwy, kodu albo opisu")
	}

	rekordy, err := s.klient.ListProducts(ctx)
	if err != nil {
		return nil, WyjscieProdukty{}, bladDlaModelu("odczyt kartoteki produktów", err)
	}

	dopasowane := filtruj(rekordy, fraza)
	wy := WyjscieProdukty{
		Znaleziono:    len(dopasowane),
		LacznieWBazie: len(rekordy),
	}
	if len(dopasowane) > MaxWynikowWyszukiwania {
		dopasowane = dopasowane[:MaxWynikowWyszukiwania]
		wy.Przyciete = true
	}
	for _, r := range dopasowane {
		wy.Produkty = append(wy.Produkty, Produkt{
			ID:        r.ID,
			Nazwa:     r.Nazwa(),
			CenaNetto: r.Pole("cena_netto", "cena", "cena_sprzedazy", "cena_netto_sprzedazy"),
			Jednostka: r.Pole("jednostka", "jm", "jedn"),
			StawkaVAT: r.Pole("stawka_vat", "vat", "stawka"),
		})
	}

	if len(wy.Produkty) == 0 {
		return tekst(fmt.Sprintf(
			"Nie znaleziono produktu pasującego do %q (przeszukano %d pozycji kartoteki). "+
				"Pozycję faktury można też opisać samym tekstem, bez wskazywania produktu z kartoteki.",
			fraza, len(rekordy))), wy, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Znaleziono %d produktów pasujących do %q", wy.Znaleziono, fraza)
	if wy.Przyciete {
		fmt.Fprintf(&b, " (pokazuję pierwszych %d)", MaxWynikowWyszukiwania)
	}
	b.WriteString(":\n")
	for _, p := range wy.Produkty {
		fmt.Fprintf(&b, "  id_produktu=%s — %s", p.ID, p.Nazwa)
		if p.CenaNetto != "" {
			fmt.Fprintf(&b, ", cena netto %s", p.CenaNetto)
		}
		if p.Jednostka != "" {
			fmt.Fprintf(&b, " / %s", p.Jednostka)
		}
		if p.StawkaVAT != "" {
			fmt.Fprintf(&b, ", VAT %s", p.StawkaVAT)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nUwaga: stawkę VAT w pozycji faktury podaje się jako procent (np. 23) albo zw — " +
		"serwer sam zamieni ją na ID stawki w Systim.")
	return tekst(b.String()), wy, nil
}

// --- filtrowanie ---

// filtruj zwraca rekordy pasujące do frazy, posortowane od najlepszego dopasowania.
func filtruj(rekordy []systim.Rekord, fraza string) []systim.Rekord {
	szukanaOgolna := normalizujTekst(fraza)
	szukanaCyfry := tylkoCyfry(fraza)

	type wynik struct {
		rekord systim.Rekord
		waga   int
	}
	var wyniki []wynik

	for _, r := range rekordy {
		waga := dopasowanie(r, szukanaOgolna, szukanaCyfry)
		if waga > 0 {
			wyniki = append(wyniki, wynik{rekord: r, waga: waga})
		}
	}

	// Sortujemy malejąco po wadze; przy równej wadze zachowujemy kolejność z kartoteki.
	sort.SliceStable(wyniki, func(i, j int) bool { return wyniki[i].waga > wyniki[j].waga })

	out := make([]systim.Rekord, 0, len(wyniki))
	for _, w := range wyniki {
		out = append(out, w.rekord)
	}
	return out
}

// dopasowanie ocenia, jak dobrze rekord pasuje do frazy. Zero oznacza brak dopasowania.
func dopasowanie(r systim.Rekord, szukanaOgolna, szukanaCyfry string) int {
	nazwa := normalizujTekst(r.Nazwa())

	// NIP porównujemy po samych cyfrach, żeby myślniki i spacje nie miały znaczenia.
	if len(szukanaCyfry) >= 7 {
		if nip := tylkoCyfry(r.NIP()); nip != "" && nip == szukanaCyfry {
			return 100
		}
	}
	if szukanaOgolna == "" {
		return 0
	}
	switch {
	case nazwa == szukanaOgolna:
		return 90
	case strings.HasPrefix(nazwa, szukanaOgolna):
		return 80
	case strings.Contains(nazwa, szukanaOgolna):
		return 70
	}
	// Na koniec przeszukujemy wszystkie pozostałe pola rekordu — kod produktu,
	// opis, adres i cokolwiek jeszcze Systim zwrócił.
	if strings.Contains(normalizujTekst(r.TekstDoSzukania()), szukanaOgolna) {
		return 40
	}
	// Częściowy NIP jako ostatnia szansa.
	if len(szukanaCyfry) >= 4 {
		if nip := tylkoCyfry(r.NIP()); nip != "" && strings.Contains(nip, szukanaCyfry) {
			return 30
		}
	}
	return 0
}

// normalizujTekst sprowadza tekst do postaci porównywalnej: małe litery, bez
// znaków interpunkcyjnych i nadmiarowych spacji.
func normalizujTekst(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	b.Grow(len(s))
	poprzedniaSpacja := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			poprzedniaSpacja = false
		case unicode.IsSpace(r):
			if !poprzedniaSpacja && b.Len() > 0 {
				b.WriteByte(' ')
				poprzedniaSpacja = true
			}
		default:
			// Myślniki, kropki i przecinki pomijamy — dzięki temu "sp. z o.o."
			// i "sp z oo" dopasowują się tak samo.
		}
	}
	return strings.TrimSpace(b.String())
}

// tylkoCyfry zostawia z tekstu same cyfry — używane przy porównywaniu NIP-ów.
func tylkoCyfry(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sklejAdres buduje jednolinijkowy adres z pól, które zwrócił Systim.
func sklejAdres(r systim.Rekord) string {
	czesci := []string{
		r.Pole("ulica", "adres", "adres1"),
		strings.TrimSpace(r.Pole("kod_pocztowy", "kod") + " " + r.Pole("miasto", "miejscowosc")),
	}
	var niepuste []string
	for _, c := range czesci {
		if c = strings.TrimSpace(c); c != "" {
			niepuste = append(niepuste, c)
		}
	}
	return strings.Join(niepuste, ", ")
}
