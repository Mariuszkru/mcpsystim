package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mariuszkru/mcpsystim/internal/invoicing"
	"github.com/Mariuszkru/mcpsystim/internal/systim"
)

// --- przygotuj_fakture ---

// WejsciePozycja to jedna pozycja dokumentu podana przez użytkownika.
type WejsciePozycja struct {
	Opis      string `json:"opis" jsonschema:"Nazwa towaru lub usługi, tak jak ma się pojawić na dokumencie"`
	Ilosc     string `json:"ilosc" jsonschema:"Ilość jako liczba z kropką dziesiętną, np. 1, 10 albo 2.5. Musi być większa od zera"`
	CenaNetto string `json:"cena_netto" jsonschema:"Cena jednostkowa netto w złotych, jako liczba z kropką dziesiętną, np. 200 albo 33.33. Nie podawaj symbolu waluty"`
	StawkaVAT string `json:"stawka_vat" jsonschema:"Stawka VAT jako procent bez znaku procenta, np. 23, 8, 5, 0, albo oznaczenie zw dla zwolnionej. Serwer sam zamieni ją na ID stawki wymagane przez API Systim"`

	Jednostka  string `json:"jednostka,omitempty" jsonschema:"Jednostka miary, np. szt, h, kg, usł. Opcjonalne"`
	IDProduktu string `json:"id_produktu,omitempty" jsonschema:"ID produktu z kartoteki Systim, zwrócone przez szukaj_produktu. Opcjonalne — pozycję można opisać samym tekstem"`
}

// WejsciePrzygotuj to parametry narzędzia przygotuj_fakture.
type WejsciePrzygotuj struct {
	IDKontrahenta   string           `json:"id_kontrahenta" jsonschema:"ID kontrahenta w Systim, zwrócone przez szukaj_kontrahenta"`
	DataWystawienia string           `json:"data_wystawienia" jsonschema:"Data wystawienia dokumentu w formacie RRRR-MM-DD, np. 2026-07-24"`
	Pozycje         []WejsciePozycja `json:"pozycje" jsonschema:"Pozycje dokumentu. Musi być co najmniej jedna"`

	DataSprzedazy string `json:"data_sprzedazy,omitempty" jsonschema:"Data sprzedaży w formacie RRRR-MM-DD. Jeśli pominięta, przyjmowana jest data wystawienia"`
	Rodzaj        int    `json:"rodzaj,omitempty" jsonschema:"Rodzaj dokumentu: 0 faktura VAT (domyślnie), 1 pro forma, 22 rachunek, 15 paragon, 26 oferta. Rodzaj 6 (paragon fiskalny) wymaga wersji Pro programu Systim i na zwykłym koncie zostanie odrzucony. Faktury w walucie obcej nie są obsługiwane"`

	TerminPlatnosci int    `json:"termin_platnosci,omitempty" jsonschema:"Termin płatności liczony w DNIACH od daty wystawienia, np. 14. Nie podawaj daty"`
	FormaPlatnosci  string `json:"forma_platnosci,omitempty" jsonschema:"Forma płatności — jedna z: przelew, gotówka, barter, za pobraniem, rozliczenie saldami, karta płatnicza. Gdy pominiesz to pole, użyta zostanie domyślna forma z konfiguracji serwera, a jeśli i tej nie ma — wartość domyślna Systim, czyli gotówka"`
	Uwagi           string `json:"uwagi,omitempty" jsonschema:"Dodatkowe uwagi drukowane na dokumencie"`
	Rabat           string `json:"rabat,omitempty" jsonschema:"Rabat na całym dokumencie jako procent bez znaku procenta, np. 5 albo 12.5. Obniża cenę jednostkową każdej pozycji"`
	WyslijDoKSeF    bool   `json:"wyslij_do_ksef,omitempty" jsonschema:"Czy po wystawieniu wysłać dokument do KSeF"`
}

// PozycjaPodgladu to pozycja z policzonymi kwotami. Kwoty są stringami z dwoma
// miejscami po przecinku — dzięki temu nie przechodzą przez float i model widzi
// dokładnie te wartości, które trafią na dokument.
type PozycjaPodgladu struct {
	Lp          int    `json:"lp" jsonschema:"Numer pozycji na dokumencie"`
	Opis        string `json:"opis" jsonschema:"Nazwa towaru lub usługi"`
	Ilosc       string `json:"ilosc" jsonschema:"Ilość"`
	Jednostka   string `json:"jednostka,omitempty" jsonschema:"Jednostka miary"`
	CenaNetto   string `json:"cena_netto" jsonschema:"Cena jednostkowa netto po ewentualnym rabacie"`
	KwotaNetto  string `json:"kwota_netto" jsonschema:"Wartość netto pozycji"`
	StawkaVAT   string `json:"stawka_vat" jsonschema:"Stawka VAT jako procent albo oznaczenie zw"`
	KwotaVAT    string `json:"kwota_vat" jsonschema:"Kwota VAT pozycji"`
	KwotaBrutto string `json:"kwota_brutto" jsonschema:"Wartość brutto pozycji"`
}

// WierszStawki to jedna linia zestawienia według stawek VAT.
type WierszStawki struct {
	StawkaVAT string `json:"stawka_vat" jsonschema:"Stawka VAT"`
	Netto     string `json:"netto" jsonschema:"Suma netto w tej stawce"`
	VAT       string `json:"vat" jsonschema:"Suma VAT w tej stawce"`
	Brutto    string `json:"brutto" jsonschema:"Suma brutto w tej stawce"`
}

// WyjsciePrzygotuj to odpowiedź narzędzia przygotuj_fakture.
type WyjsciePrzygotuj struct {
	SzkicID string `json:"szkic_id" jsonschema:"Identyfikator szkicu — przekaż go do zatwierdz_fakture, żeby wystawić dokument. Szkic jest podpisany i zawiera w sobie wszystkie dane, więc nie da się go zmodyfikować"`
	WaznyDo string `json:"wazny_do" jsonschema:"Moment wygaśnięcia szkicu w formacie RFC 3339"`

	Rodzaj           string `json:"rodzaj" jsonschema:"Rodzaj dokumentu, który zostanie wystawiony"`
	IDKontrahenta    string `json:"id_kontrahenta" jsonschema:"ID odbiorcy dokumentu"`
	NazwaKontrahenta string `json:"nazwa_kontrahenta,omitempty" jsonschema:"Nazwa odbiorcy odczytana z kartoteki"`
	DataWystawienia  string `json:"data_wystawienia" jsonschema:"Data wystawienia"`
	DataSprzedazy    string `json:"data_sprzedazy" jsonschema:"Data sprzedaży"`
	TerminPlatnosci  string `json:"termin_platnosci,omitempty" jsonschema:"Termin płatności wraz z wyliczoną datą"`
	FormaPlatnosci   string `json:"forma_platnosci,omitempty" jsonschema:"Forma płatności"`
	Uwagi            string `json:"uwagi,omitempty" jsonschema:"Uwagi, które zostaną wydrukowane na dokumencie"`

	Pozycje      []PozycjaPodgladu `json:"pozycje" jsonschema:"Pozycje z policzonymi kwotami"`
	WedlugStawek []WierszStawki    `json:"wedlug_stawek" jsonschema:"Zestawienie sum według stawek VAT"`
	RazemNetto   string            `json:"razem_netto" jsonschema:"Suma netto całego dokumentu"`
	RazemVAT     string            `json:"razem_vat" jsonschema:"Suma VAT całego dokumentu"`
	RazemBrutto  string            `json:"razem_brutto" jsonschema:"Suma brutto całego dokumentu — kwota do zapłaty"`

	Ostrzezenia  []string `json:"ostrzezenia,omitempty" jsonschema:"Sprawy, na które użytkownik powinien zwrócić uwagę przed zatwierdzeniem"`
	NastepnyKrok string   `json:"nastepny_krok" jsonschema:"Co zrobić dalej"`
}

func (s *Serwer) przygotujFakture(ctx context.Context, _ *mcp.CallToolRequest, we WejsciePrzygotuj) (*mcp.CallToolResult, WyjsciePrzygotuj, error) {
	if err := systim.SprawdzRodzaj(we.Rodzaj); err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}
	if strings.TrimSpace(we.IDKontrahenta) == "" {
		return nil, WyjsciePrzygotuj{}, errors.New(
			"pole id_kontrahenta jest wymagane — znajdź kontrahenta narzędziem szukaj_kontrahenta")
	}
	dataWystawienia, err := sprawdzDate(strings.TrimSpace(we.DataWystawienia), "data_wystawienia")
	if err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}
	dataSprzedazy := strings.TrimSpace(we.DataSprzedazy)
	if dataSprzedazy == "" {
		dataSprzedazy = dataWystawienia
	} else if dataSprzedazy, err = sprawdzDate(dataSprzedazy, "data_sprzedazy"); err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}
	if len(we.Pozycje) == 0 {
		return nil, WyjsciePrzygotuj{}, errors.New("dokument musi mieć co najmniej jedną pozycję")
	}
	if len(we.Pozycje) > s.cfg.MaxPozycji {
		return nil, WyjsciePrzygotuj{}, fmt.Errorf(
			"dokument ma %d pozycji, a limit to %d. Podziel go na kilka dokumentów",
			len(we.Pozycje), s.cfg.MaxPozycji)
	}
	// Pominięcie pola oznaczałoby wartość domyślną Systim (gotówka), co przy
	// firmach rozliczających się przelewem jest cichą pomyłką na dokumencie.
	formaPlatnosci := strings.TrimSpace(we.FormaPlatnosci)
	if formaPlatnosci == "" {
		formaPlatnosci = s.cfg.DomyslnaFormaPlatnosci
	}
	if formaPlatnosci != "" && !dozwolonaFormaPlatnosci(formaPlatnosci) {
		return nil, WyjsciePrzygotuj{}, fmt.Errorf(
			"forma płatności %q nie jest obsługiwana; dozwolone: %s",
			formaPlatnosci, strings.Join(systim.FormyPlatnosci, ", "))
	}
	if we.TerminPlatnosci < 0 {
		return nil, WyjsciePrzygotuj{}, errors.New("termin_platnosci to liczba dni i nie może być ujemny")
	}
	// Numerację sprawdzamy już na etapie podglądu, żeby brak wpisu dla tego rodzaju
	// dokumentu wyszedł tutaj, a nie dopiero przy nieodwracalnym zatwierdzeniu.
	if _, err := s.cfg.Numeracja(we.Rodzaj); err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}
	if _, err := s.cfg.Szablon(we.Rodzaj); err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}

	wejsciowe := make([]invoicing.PozycjaWejsciowa, 0, len(we.Pozycje))
	for _, p := range we.Pozycje {
		wejsciowe = append(wejsciowe, invoicing.PozycjaWejsciowa{
			IDProduktu: p.IDProduktu,
			Opis:       p.Opis,
			Ilosc:      p.Ilosc,
			Jednostka:  p.Jednostka,
			CenaNetto:  p.CenaNetto,
			StawkaVAT:  p.StawkaVAT,
		})
	}

	obliczone, sumy, err := invoicing.Oblicz(wejsciowe, we.Rabat, s.stawki)
	if err != nil {
		return nil, WyjsciePrzygotuj{}, err
	}

	// Nazwę kontrahenta odczytujemy do podglądu, żeby użytkownik zobaczył, komu
	// wystawia dokument, a nie samo ID. Brak nazwy nie blokuje przygotowania szkicu.
	nazwaKontrahenta, ostrzezenieKontrahent := s.nazwaKontrahenta(ctx, we.IDKontrahenta)

	dok := invoicing.Dokument{
		IDKontrahenta:    strings.TrimSpace(we.IDKontrahenta),
		NazwaKontrahenta: nazwaKontrahenta,
		DataWystawienia:  dataWystawienia,
		DataSprzedazy:    dataSprzedazy,
		Rodzaj:           we.Rodzaj,
		RodzajNazwa:      systim.NazwaRodzaju(we.Rodzaj),
		Pozycje:          obliczone,
		Podsumowanie:     sumy,
		TerminPlatnosci:  we.TerminPlatnosci,
		FormaPlatnosci:   formaPlatnosci,
		Uwagi:            we.Uwagi,
		Rabat:            we.Rabat,
		WyslijDoKSeF:     we.WyslijDoKSeF,
	}

	szkicID, err := s.szkice.Podpisz(dok)
	if err != nil {
		return nil, WyjsciePrzygotuj{}, fmt.Errorf("nie udało się przygotować szkicu: %w", err)
	}

	wy := WyjsciePrzygotuj{
		SzkicID:          szkicID,
		WaznyDo:          time.Now().Add(invoicing.TTLSzkicu).Format(time.RFC3339),
		Rodzaj:           systim.NazwaRodzaju(we.Rodzaj),
		IDKontrahenta:    dok.IDKontrahenta,
		NazwaKontrahenta: nazwaKontrahenta,
		DataWystawienia:  dataWystawienia,
		DataSprzedazy:    dataSprzedazy,
		FormaPlatnosci:   formaPlatnosci,
		Uwagi:            strings.TrimSpace(we.Uwagi),
		RazemNetto:       sumy.Netto.StringFixed(2),
		RazemVAT:         sumy.VAT.StringFixed(2),
		RazemBrutto:      sumy.Brutto.StringFixed(2),
		NastepnyKrok: "Pokaż użytkownikowi powyższe kwoty i poproś o potwierdzenie. " +
			"Dopiero po wyraźnej zgodzie wywołaj zatwierdz_fakture z tym szkic_id. " +
			"Wystawienie dokumentu jest nieodwracalne.",
	}
	if we.TerminPlatnosci > 0 {
		termin, _ := time.Parse(formatDaty, dataWystawienia)
		wy.TerminPlatnosci = fmt.Sprintf("%d dni (do %s)",
			we.TerminPlatnosci, termin.AddDate(0, 0, we.TerminPlatnosci).Format(formatDaty))
	}
	for i, p := range obliczone {
		wy.Pozycje = append(wy.Pozycje, PozycjaPodgladu{
			Lp:          i + 1,
			Opis:        p.Opis,
			Ilosc:       p.Ilosc.String(),
			Jednostka:   p.Jednostka,
			CenaNetto:   p.CenaNetto.StringFixed(2),
			KwotaNetto:  p.KwotaNetto.StringFixed(2),
			StawkaVAT:   p.StawkaVAT,
			KwotaVAT:    p.KwotaVAT.StringFixed(2),
			KwotaBrutto: p.KwotaBrutto.StringFixed(2),
		})
	}
	for _, w := range sumy.WedlugStaw {
		wy.WedlugStawek = append(wy.WedlugStawek, WierszStawki{
			StawkaVAT: w.StawkaVAT,
			Netto:     w.Netto.StringFixed(2),
			VAT:       w.VAT.StringFixed(2),
			Brutto:    w.Brutto.StringFixed(2),
		})
	}

	if ostrzezenieKontrahent != "" {
		wy.Ostrzezenia = append(wy.Ostrzezenia, ostrzezenieKontrahent)
	}
	if we.Rabat != "" {
		wy.Ostrzezenia = append(wy.Ostrzezenia, fmt.Sprintf(
			"Zastosowano rabat %s%%. Ceny jednostkowe w podglądzie są już po rabacie, "+
				"bo API Systim nie przelicza kwot samo. Samo pole „rabat” nie jest wysyłane "+
				"do Systim (wywraca backend przy 3+ pozycjach), więc na wydruku nie pojawi "+
				"się osobna adnotacja o rabacie — będą tylko obniżone ceny.", we.Rabat))
	}
	if we.WyslijDoKSeF {
		wy.Ostrzezenia = append(wy.Ostrzezenia,
			"Dokument zostanie wysłany do KSeF zaraz po wystawieniu.")
	}
	// Ostrzegamy tylko wtedy, gdy forma faktycznie nie trafi na dokument.
	if _, ok := s.cfg.FormaPlatnosciID(formaPlatnosci); formaPlatnosci != "" && !ok {
		wy.Ostrzezenia = append(wy.Ostrzezenia, fmt.Sprintf(
			"Forma płatności %q nie ma przypisanego ID, więc NIE zostanie zapisana na "+
				"dokumencie — Systim wstawi gotówkę. Uzupełnij SYSTIM_FORMY_PLATNOSCI.",
			formaPlatnosci))
	}
	// if we.Rodzaj == systim.RodzajFakturaVAT {
	// 	wy.Ostrzezenia = append(wy.Ostrzezenia,
	// 		"Przy pierwszym uruchomieniu na nowym koncie wystaw najpierw pro formę (rodzaj 1) "+
	// 			"i sprawdź dokument w panelu, zanim wystawisz fakturę VAT.")
	// }

	return tekst(podgladTekstowy(wy)), wy, nil
}

// nazwaKontrahenta odczytuje nazwę odbiorcy z kartoteki. Zwraca też ostrzeżenie,
// gdy kontrahenta nie udało się potwierdzić.
func (s *Serwer) nazwaKontrahenta(ctx context.Context, id string) (string, string) {
	rekordy, err := s.klient.ListCompanies(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "nie udało się odczytać kartoteki kontrahentów do podglądu", "blad", err)
		return "", "Nie udało się potwierdzić kontrahenta w kartotece Systim, więc podgląd pokazuje " +
			"samo ID. Sprawdź, czy id_kontrahenta jest poprawne — API odrzuci dokument ze złym ID."
	}
	for _, r := range rekordy {
		if r.ID == id {
			return r.Nazwa(), ""
		}
	}
	return "", fmt.Sprintf(
		"W kartotece Systim nie ma kontrahenta o ID %s. Zatwierdzenie zakończy się błędem — "+
			"znajdź właściwe ID narzędziem szukaj_kontrahenta.", id)
}

// podgladTekstowy składa czytelny podgląd dokumentu dla użytkownika.
func podgladTekstowy(w WyjsciePrzygotuj) string {
	var b strings.Builder
	b.WriteString("PODGLĄD DOKUMENTU — nic jeszcze nie zostało zapisane w Systim.\n\n")
	fmt.Fprintf(&b, "Rodzaj:      %s\n", w.Rodzaj)
	if w.NazwaKontrahenta != "" {
		fmt.Fprintf(&b, "Nabywca:     %s (id %s)\n", w.NazwaKontrahenta, w.IDKontrahenta)
	} else {
		fmt.Fprintf(&b, "Nabywca:     id_kontrahenta = %s\n", w.IDKontrahenta)
	}
	fmt.Fprintf(&b, "Wystawienie: %s\n", w.DataWystawienia)
	fmt.Fprintf(&b, "Sprzedaż:    %s\n", w.DataSprzedazy)
	if w.TerminPlatnosci != "" {
		fmt.Fprintf(&b, "Termin:      %s\n", w.TerminPlatnosci)
	}
	if w.FormaPlatnosci != "" {
		fmt.Fprintf(&b, "Płatność:    %s\n", w.FormaPlatnosci)
	}
	if w.Uwagi != "" {
		fmt.Fprintf(&b, "Uwagi:       %s\n", w.Uwagi)
	}

	b.WriteString("\nPozycje:\n")
	for _, p := range w.Pozycje {
		jedn := p.Jednostka
		if jedn != "" {
			jedn = " " + jedn
		}
		fmt.Fprintf(&b, "  %d. %s\n", p.Lp, p.Opis)
		fmt.Fprintf(&b, "     %s%s × %s zł = %s zł netto, VAT %s (%s zł), brutto %s zł\n",
			p.Ilosc, jedn, p.CenaNetto, p.KwotaNetto, p.StawkaVAT, p.KwotaVAT, p.KwotaBrutto)
	}

	if len(w.WedlugStawek) > 1 {
		b.WriteString("\nWedług stawek:\n")
		for _, s := range w.WedlugStawek {
			fmt.Fprintf(&b, "  %s: netto %s zł, VAT %s zł, brutto %s zł\n",
				s.StawkaVAT, s.Netto, s.VAT, s.Brutto)
		}
	}

	fmt.Fprintf(&b, "\nRAZEM netto:  %s zł\n", w.RazemNetto)
	fmt.Fprintf(&b, "RAZEM VAT:    %s zł\n", w.RazemVAT)
	fmt.Fprintf(&b, "DO ZAPŁATY:   %s zł\n", w.RazemBrutto)

	if len(w.Ostrzezenia) > 0 {
		b.WriteString("\nZwróć uwagę:\n")
		for _, o := range w.Ostrzezenia {
			fmt.Fprintf(&b, "  • %s\n", o)
		}
	}

	fmt.Fprintf(&b, "\nSzkic ważny do %s.\n", w.WaznyDo)
	// szkic_id musi być w tekście, a nie tylko w StructuredContent: część klientów
	// MCP podaje modelowi wyłącznie Content, a tej wartości nie da się odtworzyć —
	// jest podpisana. Bez niej podgląd powstaje, ale dokumentu nie da się wystawić.
	fmt.Fprintf(&b, "szkic_id: %s\n", w.SzkicID)
	b.WriteString("Aby wystawić dokument, potwierdź kwoty z użytkownikiem i wywołaj " +
		"zatwierdz_fakture z podanym wyżej szkic_id. Operacja jest nieodwracalna.")
	return b.String()
}

func dozwolonaFormaPlatnosci(f string) bool {
	for _, d := range systim.FormyPlatnosci {
		if strings.EqualFold(strings.TrimSpace(f), d) {
			return true
		}
	}
	return false
}

// --- zatwierdz_fakture ---

// WejscieZatwierdz to parametry narzędzia zatwierdz_fakture.
type WejscieZatwierdz struct {
	SzkicID string `json:"szkic_id" jsonschema:"Identyfikator szkicu zwrócony przez przygotuj_fakture. Nie modyfikuj go ani nie twórz własnego — jest podpisany kryptograficznie"`

	WyslijEmail bool   `json:"wyslij_email,omitempty" jsonschema:"Czy Systim ma wysłać dokument e-mailem do kontrahenta zaraz po wystawieniu"`
	Email       string `json:"email,omitempty" jsonschema:"Adres e-mail odbiorcy. Wymagany, gdy wyslij_email jest ustawione na true"`
}

// WyjscieZatwierdz to odpowiedź narzędzia zatwierdz_fakture.
type WyjscieZatwierdz struct {
	IDFaktury   string `json:"id_faktury" jsonschema:"ID wystawionego dokumentu w Systim — użyj go w pobierz_pdf"`
	Numer       string `json:"numer" jsonschema:"Nadany numer dokumentu"`
	RazemBrutto string `json:"razem_brutto" jsonschema:"Kwota brutto wystawionego dokumentu"`

	Ksiegowanie    string `json:"ksiegowanie" jsonschema:"Wynik automatycznego księgowania, wyjaśniony słownie"`
	WymagaUwagi    bool   `json:"wymaga_uwagi" jsonschema:"Czy wynik księgowania wymaga reakcji użytkownika — przy true koniecznie przekaż mu treść pola ksiegowanie"`
	KsefData       string `json:"ksef_data,omitempty" jsonschema:"Dane zwrócone przez KSeF, jeśli dokument został tam wysłany"`
	WyslanoEmailem bool   `json:"wyslano_emailem" jsonschema:"Czy zlecono wysyłkę dokumentu e-mailem"`
}

func (s *Serwer) zatwierdzFakture(ctx context.Context, _ *mcp.CallToolRequest, we WejscieZatwierdz) (*mcp.CallToolResult, WyjscieZatwierdz, error) {
	dok, err := s.szkice.Zweryfikuj(we.SzkicID)
	if err != nil {
		// Komunikaty z pakietu invoicing są już po polsku i mówią, co zrobić dalej.
		return nil, WyjscieZatwierdz{}, err
	}
	if we.WyslijEmail && strings.TrimSpace(we.Email) == "" {
		return nil, WyjscieZatwierdz{}, errors.New(
			"wysyłka e-mailem wymaga podania adresu w polu email")
	}

	// Numeracja jest wybierana według rodzaju dokumentu zapisanego w szkicu,
	// a nie według tego, co akurat stoi w konfiguracji jako pierwsze.
	idNumeracji, err := s.cfg.Numeracja(dok.Rodzaj)
	if err != nil {
		return nil, WyjscieZatwierdz{}, err
	}
	idSzablonu, err := s.cfg.Szablon(dok.Rodzaj)
	if err != nil {
		return nil, WyjscieZatwierdz{}, err
	}

	// Do API idzie ID formy płatności, nie jej nazwa. Brak mapowania oznacza,
	// że pola nie wysyłamy — użytkownik został o tym ostrzeżony w podglądzie.
	idFormyPlatnosci, _ := s.cfg.FormaPlatnosciID(dok.FormaPlatnosci)

	zadanie := systim.ZadanieFaktury{
		IDKontrahenta:   dok.IDKontrahenta,
		DataWystawienia: dok.DataWystawienia,
		DataSprzedazy:   dok.DataSprzedazy,
		Rodzaj:          dok.Rodzaj,
		IDSzablonu:      idSzablonu,
		IDNumeracji:     idNumeracji,
		Pozycje:         invoicing.NaPozycjeSystim(dok.Pozycje),
		TerminPlatnosci: dok.TerminPlatnosci,
		FormaPlatnosci:  idFormyPlatnosci,
		Uwagi:           dok.Uwagi,
		Rabat:           dok.Rabat,
		WyslijEmail:     we.WyslijEmail,
		EmailAdres:      strings.TrimSpace(we.Email),
		WyslijDoKSeF:    dok.WyslijDoKSeF,
	}

	s.log.InfoContext(ctx, "wystawiam dokument w Systim",
		"rodzaj", dok.Rodzaj,
		"id_kontrahenta", dok.IDKontrahenta,
		"pozycji", len(dok.Pozycje),
		"brutto", dok.Podsumowanie.Brutto.StringFixed(2),
	)

	wynik, err := s.klient.AddSellInvoice(ctx, zadanie)
	if err != nil {
		return nil, WyjscieZatwierdz{}, bladDlaModelu("wystawienie dokumentu", err)
	}

	// Systim nie zawsze zwraca numer w odpowiedzi addSellInvoice — potwierdzone
	// na pro formie, gdzie dokument dostał numer, a pole wróciło puste. Numer jest
	// jednak nadany, a użytkownik musi go poznać, więc doczytujemy go po ID.
	numer := wynik.Numer
	if numer == "" && wynik.ID != "" {
		numer = s.doczytajNumer(ctx, wynik.ID)
	}

	opisKsiegowania, wymagaUwagi := systim.OpisResultCode(wynik.ResultCode)
	wy := WyjscieZatwierdz{
		IDFaktury:      wynik.ID,
		Numer:          numer,
		RazemBrutto:    dok.Podsumowanie.Brutto.StringFixed(2),
		Ksiegowanie:    opisKsiegowania,
		WymagaUwagi:    wymagaUwagi,
		KsefData:       wynik.KsefData,
		WyslanoEmailem: we.WyslijEmail,
	}

	s.log.InfoContext(ctx, "dokument wystawiony",
		"id", wynik.ID, "numer", numer, "result_code", wynik.ResultCode)

	var b strings.Builder
	fmt.Fprintf(&b, "Dokument został wystawiony w Systim.\n\n")
	fmt.Fprintf(&b, "Numer:       %s\n", opisNumeru(numer))
	fmt.Fprintf(&b, "ID:          %s\n", wynik.ID)
	fmt.Fprintf(&b, "Do zapłaty:  %s zł\n", wy.RazemBrutto)
	if dok.NazwaKontrahenta != "" {
		fmt.Fprintf(&b, "Nabywca:     %s\n", dok.NazwaKontrahenta)
	}
	// O księgowaniu mówimy tylko wtedy, gdy coś poszło nie tak. Komunikat
	// „księgowanie wyłączone" albo „utworzono zapis" pojawiałby się przy każdym
	// dokumencie i niczego nie wnosi, natomiast result_code 102 oznacza dokument
	// wystawiony bez zapisu w księgowości — to użytkownik musi zobaczyć.
	if wymagaUwagi {
		fmt.Fprintf(&b, "\nKsięgowanie: %s\n", opisKsiegowania)
	}
	if wynik.KsefData != "" {
		fmt.Fprintf(&b, "KSeF:        %s\n", wynik.KsefData)
	}
	if we.WyslijEmail {
		fmt.Fprintf(&b, "E-mail:      zlecono wysyłkę na %s\n", we.Email)
	}

	return tekst(b.String()), wy, nil
}

// doczytajNumer odczytuje numer wystawionego dokumentu po jego ID.
//
// Wywoływane tylko wtedy, gdy addSellInvoice nie zwróciło numeru. Błąd odczytu
// nie może przewrócić operacji — dokument już istnieje, więc gorszym wynikiem
// jest brak numeru w odpowiedzi niż zgłoszenie porażki wystawienia.
func (s *Serwer) doczytajNumer(ctx context.Context, id string) string {
	rekordy, err := s.klient.ListSellInvoices(ctx, systim.FiltrFaktur{IDs: []string{id}})
	if err != nil {
		s.log.WarnContext(ctx, "nie udało się doczytać numeru wystawionego dokumentu",
			"id", id, "blad", err.Error())
		return ""
	}
	for _, r := range rekordy {
		if r.ID == id {
			return r.Pole("numer", "numer_faktury", "nr")
		}
	}
	return ""
}

// opisNumeru zwraca numer albo wyjaśnienie, gdy go nie poznaliśmy.
func opisNumeru(numer string) string {
	if numer != "" {
		return numer
	}
	return "(Systim nie zwróciło numeru — sprawdź dokument w panelu po jego ID)"
}

// --- lista_faktur ---

// WejscieListaFaktur to parametry narzędzia lista_faktur.
type WejscieListaFaktur struct {
	DataOd string `json:"data_od" jsonschema:"Początek zakresu dat wystawienia, format RRRR-MM-DD, np. 2026-07-01"`
	DataDo string `json:"data_do" jsonschema:"Koniec zakresu dat wystawienia, format RRRR-MM-DD, np. 2026-07-31"`
}

// Faktura to rekord listy faktur sprzedaży.
type Faktura struct {
	ID              string `json:"id_faktury" jsonschema:"ID dokumentu w Systim"`
	Numer           string `json:"numer" jsonschema:"Numer dokumentu"`
	DataWystawienia string `json:"data_wystawienia,omitempty" jsonschema:"Data wystawienia"`
	Kontrahent      string `json:"kontrahent,omitempty" jsonschema:"Nazwa nabywcy"`
	Brutto          string `json:"brutto,omitempty" jsonschema:"Kwota brutto dokumentu"`
	FormaPlatnosci  string `json:"forma_platnosci,omitempty" jsonschema:"Forma płatności zapisana na dokumencie"`
	TerminPlatnosci string `json:"termin_platnosci,omitempty" jsonschema:"Termin płatności zapisany na dokumencie"`
}

// WyjscieListaFaktur to odpowiedź narzędzia lista_faktur.
type WyjscieListaFaktur struct {
	Faktury []Faktura `json:"faktury" jsonschema:"Faktury wystawione w podanym zakresie dat"`
	Liczba  int       `json:"liczba" jsonschema:"Liczba znalezionych dokumentów"`
}

func (s *Serwer) listaFaktur(ctx context.Context, _ *mcp.CallToolRequest, we WejscieListaFaktur) (*mcp.CallToolResult, WyjscieListaFaktur, error) {
	od, err := sprawdzDate(strings.TrimSpace(we.DataOd), "data_od")
	if err != nil {
		return nil, WyjscieListaFaktur{}, err
	}
	do, err := sprawdzDate(strings.TrimSpace(we.DataDo), "data_do")
	if err != nil {
		return nil, WyjscieListaFaktur{}, err
	}
	if od > do {
		return nil, WyjscieListaFaktur{}, fmt.Errorf(
			"data_od (%s) jest późniejsza niż data_do (%s)", od, do)
	}

	rekordy, err := s.klient.ListSellInvoices(ctx, systim.FiltrFaktur{DataOd: od, DataDo: do})
	if err != nil {
		return nil, WyjscieListaFaktur{}, bladDlaModelu("odczyt listy faktur", err)
	}

	wy := WyjscieListaFaktur{Liczba: len(rekordy)}
	for _, r := range rekordy {
		wy.Faktury = append(wy.Faktury, Faktura{
			ID:              r.ID,
			Numer:           r.Pole("numer", "numer_faktury", "nr"),
			DataWystawienia: r.Pole("data_wystawienia", "data"),
			Kontrahent:      r.Pole("kontrahent", "nazwa_kontrahenta", "nabywca", "nazwa"),
			Brutto:          r.Pole("kwota_brutto", "brutto", "razem_brutto", "suma_brutto"),
			FormaPlatnosci:  r.Pole("forma_platnosci", "platnosc", "sposob_platnosci"),
			TerminPlatnosci: r.Pole("termin_platnosci", "termin", "data_platnosci"),
		})
	}

	if len(wy.Faktury) == 0 {
		return tekst(fmt.Sprintf("Brak faktur sprzedaży wystawionych między %s a %s.", od, do)), wy, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Faktury sprzedaży wystawione między %s a %s (%d):\n", od, do, len(wy.Faktury))
	for _, f := range wy.Faktury {
		fmt.Fprintf(&b, "  %s — id %s", f.Numer, f.ID)
		if f.DataWystawienia != "" {
			fmt.Fprintf(&b, ", %s", f.DataWystawienia)
		}
		if f.Kontrahent != "" {
			fmt.Fprintf(&b, ", %s", f.Kontrahent)
		}
		if f.Brutto != "" {
			fmt.Fprintf(&b, ", brutto %s zł", f.Brutto)
		}
		if f.FormaPlatnosci != "" {
			fmt.Fprintf(&b, ", %s", f.FormaPlatnosci)
		}
		if f.TerminPlatnosci != "" {
			fmt.Fprintf(&b, ", termin %s", f.TerminPlatnosci)
		}
		b.WriteByte('\n')
	}
	return tekst(b.String()), wy, nil
}

// --- pobierz_pdf ---

// WejsciePDF to parametry narzędzia pobierz_pdf.
type WejsciePDF struct {
	IDFaktury string `json:"id_faktury" jsonschema:"ID faktury w Systim, zwrócone przez zatwierdz_fakture albo lista_faktur"`
}

// WyjsciePDF to odpowiedź narzędzia pobierz_pdf.
type WyjsciePDF struct {
	Sciezka     string `json:"sciezka" jsonschema:"Pełna ścieżka do zapisanego pliku PDF na dysku serwera"`
	NazwaPliku  string `json:"nazwa_pliku" jsonschema:"Nazwa zapisanego pliku"`
	RozmiarBajt int    `json:"rozmiar_bajtow" jsonschema:"Rozmiar pliku w bajtach"`
}

func (s *Serwer) pobierzPDF(ctx context.Context, _ *mcp.CallToolRequest, we WejsciePDF) (*mcp.CallToolResult, WyjsciePDF, error) {
	id := strings.TrimSpace(we.IDFaktury)
	if id == "" {
		return nil, WyjsciePDF{}, errors.New("pole id_faktury jest wymagane")
	}

	plik, err := s.klient.GetSellInvoicePDF(ctx, id)
	if err != nil {
		return nil, WyjsciePDF{}, bladDlaModelu("pobranie PDF faktury", err)
	}

	dane, err := base64.StdEncoding.DecodeString(strings.TrimSpace(plik.Base64))
	if err != nil {
		return nil, WyjsciePDF{}, fmt.Errorf("Systim zwróciło plik, którego nie da się odkodować z base64: %w", err)
	}
	if len(dane) == 0 {
		return nil, WyjsciePDF{}, fmt.Errorf("Systim zwróciło pusty plik dla faktury %s", id)
	}

	if err := os.MkdirAll(s.cfg.KatalogPDF, 0o750); err != nil {
		return nil, WyjsciePDF{}, fmt.Errorf(
			"nie udało się utworzyć katalogu %s na pliki PDF: %w. "+
				"Sprawdź, czy wolumen jest zamontowany i zapisywalny dla użytkownika kontenera",
			s.cfg.KatalogPDF, err)
	}

	nazwa := bezpiecznaNazwaPliku(plik.Nazwa, id)
	sciezka := filepath.Join(s.cfg.KatalogPDF, nazwa)
	if err := os.WriteFile(sciezka, dane, 0o640); err != nil {
		return nil, WyjsciePDF{}, fmt.Errorf("nie udało się zapisać pliku %s: %w", sciezka, err)
	}

	s.log.InfoContext(ctx, "zapisano PDF faktury", "id", id, "sciezka", sciezka, "bajtow", len(dane))

	wy := WyjsciePDF{Sciezka: sciezka, NazwaPliku: nazwa, RozmiarBajt: len(dane)}
	return tekst(fmt.Sprintf(
		"PDF faktury %s zapisany na dysku serwera:\n  %s\n  (%d bajtów)\n\n"+
			"Zawartość pliku nie jest zwracana w odpowiedzi, żeby nie zapychać kontekstu rozmowy.",
		id, sciezka, len(dane))), wy, nil
}

// polskieZnaki mapuje znaki diakrytyczne na odpowiedniki ASCII, żeby nazwa pliku
// pozostała czytelna zamiast zamienić się w ciąg podkreśleń.
var polskieZnaki = strings.NewReplacer(
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n", "ó", "o", "ś", "s", "ź", "z", "ż", "z",
	"Ą", "A", "Ć", "C", "Ę", "E", "Ł", "L", "Ń", "N", "Ó", "O", "Ś", "S", "Ź", "Z", "Ż", "Z",
)

// bezpiecznaNazwaPliku sprowadza nazwę zwróconą przez API do bezpiecznej nazwy pliku.
//
// Nazwa pochodzi z zewnętrznego systemu, więc nie może zawierać separatorów ścieżki
// ani sekwencji wyjścia z katalogu. Rozszerzenie odcinamy i zawsze doklejamy .pdf —
// dzięki temu nazwa nigdy nie kończy się czymś innym, niż faktycznie zapisujemy.
//
// Ukośniki z numeru dokumentu (FV/12/2026) stają się podkreśleniami, więc numer
// pozostaje rozpoznawalny, a katalog docelowy się nie rozgałęzia.
func bezpiecznaNazwaPliku(nazwa, idFaktury string) string {
	nazwa = polskieZnaki.Replace(strings.TrimSpace(nazwa))
	// Rdzeń nazwy bez rozszerzenia; rozszerzenie i tak narzucamy sami.
	rdzen := strings.TrimSuffix(nazwa, filepath.Ext(nazwa))

	var b strings.Builder
	poprzedniePodkreslenie := false
	for _, r := range rdzen {
		czyDozwolony := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_'
		if czyDozwolony && r != '_' {
			b.WriteRune(r)
			poprzedniePodkreslenie = false
			continue
		}
		// Wszystko pozostałe — w tym separatory ścieżki, kropki i spacje —
		// zamieniamy na pojedyncze podkreślenie.
		if !poprzedniePodkreslenie {
			b.WriteByte('_')
			poprzedniePodkreslenie = true
		}
	}

	oczyszczona := strings.Trim(b.String(), "_-")
	if oczyszczona == "" {
		oczyszczona = "faktura_" + tylkoCyfryLubDomyslne(idFaktury)
	}
	return oczyszczona + ".pdf"
}

func tylkoCyfryLubDomyslne(id string) string {
	if c := tylkoCyfry(id); c != "" {
		return c
	}
	return "bez_id"
}
