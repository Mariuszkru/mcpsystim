// Package tools definiuje narzędzia MCP wystawiane przez serwer.
//
// Opisy pól w tagach jsonschema są promptem dla modelu — to z nich Claude dowiaduje
// się, w jakim formacie podać datę i jakie wartości są dozwolone. Dlatego są po polsku
// i mówią konkretnie o formatach, jednostkach i skutkach.
package tools

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mkrukowski/systim-mcp/internal/config"
	"github.com/mkrukowski/systim-mcp/internal/invoicing"
	"github.com/mkrukowski/systim-mcp/internal/systim"
)

// MaxWynikowWyszukiwania ogranicza liczbę zwracanych rekordów, żeby nie zapychać
// kontekstu modelu całą kartoteką.
const MaxWynikowWyszukiwania = 25

// Serwer spina klienta Systim, przeliczenia i konfigurację w komplet narzędzi MCP.
type Serwer struct {
	klient *systim.Client
	stawki *invoicing.StawkiVAT
	szkice *invoicing.PodpisSzkicow
	cfg    *config.Config
	log    *slog.Logger
}

// NowySerwer tworzy warstwę narzędzi.
func NowySerwer(klient *systim.Client, stawki *invoicing.StawkiVAT, szkice *invoicing.PodpisSzkicow, cfg *config.Config, log *slog.Logger) *Serwer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Serwer{klient: klient, stawki: stawki, szkice: szkice, cfg: cfg, log: log}
}

// prawda i falsz pomagają ustawiać wskaźnikowe pola adnotacji.
var (
	prawda = true
	falsz  = false
)

// Zarejestruj tworzy serwer MCP z kompletem narzędzi.
//
// Adnotacje mają znaczenie: narzędzia listujące są oznaczone jako read-only,
// a zatwierdz_fakture jako destrukcyjne i nieidempotentne — wystawienie dokumentu
// księgowego jest nieodwracalne, a powtórzenie wywołania tworzy drugi dokument.
func (s *Serwer) Zarejestruj(impl *mcp.Implementation, opcje *mcp.ServerOptions) *mcp.Server {
	srv := mcp.NewServer(impl, opcje)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "lista_stawek_vat",
		Description: "Zwraca stawki VAT skonfigurowane w Systim wraz z ich ID. " +
			"Służy do jednorazowej konfiguracji serwera: odczytane ID trzeba wpisać do zmiennej " +
			"środowiskowej SYSTIM_VAT_IDS jako mapę procent → ID, na przykład " +
			`{"23":1,"8":2,"5":3,"0":4,"zw":5}. ` +
			"Pole stawka_vat w API Systim przyjmuje ID stawki, a nie procent.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Lista stawek VAT",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &prawda,
			DestructiveHint: &falsz,
		},
	}, s.listaStawekVAT)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "szukaj_kontrahenta",
		Description: "Wyszukuje kontrahenta w kartotece Systim po nazwie lub numerze NIP. " +
			"Wyszukiwanie ignoruje wielkość liter oraz myślniki i spacje w NIP-ie, " +
			"więc „1234567890”, „123-456-78-90” i „123 456 78 90” dają ten sam wynik. " +
			"Zwraca maksymalnie " + "25" + " dopasowań. " +
			"Zwrócone id_kontrahenta jest potrzebne do przygotowania faktury.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Szukaj kontrahenta",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &prawda,
			DestructiveHint: &falsz,
		},
	}, s.szukajKontrahenta)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "szukaj_produktu",
		Description: "Wyszukuje produkt lub usługę w kartotece Systim po nazwie, kodzie lub opisie. " +
			"Wyszukiwanie ignoruje wielkość liter. Zwraca maksymalnie 25 dopasowań. " +
			"Zwrócone id_produktu można podać w pozycji faktury, ale nie jest ono wymagane — " +
			"pozycję można opisać także samym tekstem.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Szukaj produktu",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &prawda,
			DestructiveHint: &falsz,
		},
	}, s.szukajProduktu)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "przygotuj_fakture",
		Description: "Przelicza pozycje dokumentu i zwraca podgląd z kwotami netto, VAT i brutto " +
			"oraz identyfikator szkicu (szkic_id). NICZEGO NIE ZAPISUJE w Systim — żaden dokument " +
			"jeszcze nie powstaje i nie jest nadawany numer. " +
			"Pokaż użytkownikowi zwrócone kwoty i poproś o ich potwierdzenie, a dopiero po wyraźnej " +
			"zgodzie wywołaj zatwierdz_fakture ze zwróconym szkic_id. " +
			"Szkic jest ważny 30 minut. Kwoty liczy ten serwer, ponieważ API Systim ich nie wylicza.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Przygotuj fakturę (podgląd, bez zapisu)",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &falsz,
			DestructiveHint: &falsz,
		},
	}, s.przygotujFakture)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "zatwierdz_fakture",
		Description: "WYSTAWIA dokument w Systim na podstawie szkicu z przygotuj_fakture. " +
			"OPERACJA JEST NIEODWRACALNA: powstaje dokument księgowy z nadanym numerem, którego " +
			"to narzędzie nie potrafi usunąć ani cofnąć. " +
			"Wywołaj je WYŁĄCZNIE po tym, jak użytkownik zobaczył kwoty z przygotuj_fakture " +
			"i wyraźnie zgodził się na wystawienie dokumentu. Nie wywołuj go „na wszelki wypadek”, " +
			"nie ponawiaj po błędzie bez zgody użytkownika i nie zgaduj szkic_id — " +
			"każde wywołanie tworzy osobny dokument.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Zatwierdź i wystaw fakturę (nieodwracalne)",
			ReadOnlyHint:    false,
			IdempotentHint:  false,
			OpenWorldHint:   &prawda,
			DestructiveHint: &prawda,
		},
	}, s.zatwierdzFakture)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "lista_faktur",
		Description: "Zwraca faktury sprzedaży wystawione w podanym zakresie dat wystawienia. " +
			"Służy do weryfikacji, czy dokument faktycznie powstał, oraz do sprawdzenia numeracji.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Lista faktur sprzedaży",
			ReadOnlyHint:    true,
			IdempotentHint:  true,
			OpenWorldHint:   &prawda,
			DestructiveHint: &falsz,
		},
	}, s.listaFaktur)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "pobierz_pdf",
		Description: "Pobiera PDF faktury sprzedaży z Systim i zapisuje go w katalogu na dysku serwera. " +
			"Zwraca ścieżkę do zapisanego pliku, a nie jego zawartość — plik PDF w odpowiedzi " +
			"zapchałby kontekst rozmowy.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Pobierz PDF faktury",
			ReadOnlyHint:    false,
			IdempotentHint:  true,
			OpenWorldHint:   &prawda,
			DestructiveHint: &falsz,
		},
	}, s.pobierzPDF)

	return srv
}
