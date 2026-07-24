package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// kluczKontekstu to typ klucza wartości w context.Context, żeby nie kolidować
// z kluczami innych pakietów.
type kluczKontekstu struct{ nazwa string }

var kluczRoszczen = kluczKontekstu{"roszczenia"}

// RoszczeniaZKontekstu zwraca roszczenia tokenu, którym uwierzytelniono żądanie.
func RoszczeniaZKontekstu(ctx context.Context) (*Roszczenia, bool) {
	r, ok := ctx.Value(kluczRoszczen).(*Roszczenia)
	return r, ok
}

// MetadaneZasobu to Protected Resource Metadata według RFC 9728.
//
// Claude pobiera ten dokument, żeby dowiedzieć się, który serwer autoryzacji
// obsługuje ten zasób, i dopiero potem rozpoczyna flow OAuth.
type MetadaneZasobu struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported,omitempty"`
	ResourceName           string   `json:"resource_name,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// Opcje konfigurują middleware uwierzytelniający.
type Opcje struct {
	// Walidator sprawdza tokeny. Nil oznacza wyłączoną walidację.
	Walidator *Walidator
	// URLMetadanych to adres, pod którym serwer hostuje Protected Resource Metadata.
	URLMetadanych string
	// Zasob to kanoniczny identyfikator chronionego zasobu.
	Zasob string
	// WymaganyScope to uprawnienie sprawdzane w tokenie.
	WymaganyScope string
	// ZadaneScopes trafiają do nagłówka WWW-Authenticate jako lista scope'ów,
	// o które klient ma poprosić serwer autoryzacji.
	//
	// To nie to samo co WymaganyScope. Klient musi zwykle poprosić o więcej, niż
	// sprawdzamy: authentik od wersji 2024.2 nie wyda refresh tokenu, jeśli
	// w żądaniu nie było scope offline_access — a bez refresh tokenu konektor
	// przestanie działać, gdy tylko wygaśnie token dostępowy.
	//
	// Puste pole oznacza, że ogłaszamy sam WymaganyScope.
	ZadaneScopes []string
	Logger       *slog.Logger
}

// ogloszonyScope buduje wartość parametru scope w nagłówku WWW-Authenticate.
func (o Opcje) ogloszonyScope() string {
	if len(o.ZadaneScopes) > 0 {
		return strings.Join(o.ZadaneScopes, " ")
	}
	return o.WymaganyScope
}

// HandlerMetadanych zwraca handler serwujący Protected Resource Metadata.
func HandlerMetadanych(m MetadaneZasobu) http.Handler {
	dane, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		// Struktura jest stała i w pełni serializowalna — błąd tu oznacza pomyłkę w kodzie.
		panic(fmt.Sprintf("serializacja metadanych zasobu: %v", err))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "metoda niedozwolona", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Dokument jest publiczny i rzadko się zmienia.
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.WriteHeader(http.StatusOK)
		w.Write(dane)
	})
}

// Wymagaj opakowuje handler walidacją tokenu dostępowego.
//
// Żądanie bez poprawnego tokenu dostaje 401 z nagłówkiem WWW-Authenticate
// wskazującym na Protected Resource Metadata — to po nim Claude odnajduje serwer
// autoryzacji i rozpoczyna flow OAuth.
func Wymagaj(nastepny http.Handler, o Opcje) http.Handler {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o.Walidator == nil {
			// Autoryzacja wyłączona — wyłącznie do testów lokalnych. Ostrzeżenie
			// przy każdym starcie wypisuje main.
			nastepny.ServeHTTP(w, r)
			return
		}

		token, err := tokenZNaglowka(r)
		if err != nil {
			odmow(w, o, http.StatusUnauthorized, "invalid_request", err)
			return
		}

		roszczenia, err := o.Walidator.Zweryfikuj(r.Context(), token)
		if err != nil {
			log.WarnContext(r.Context(), "odrzucono token dostępowy",
				"blad", err.Error(),
				"sciezka", r.URL.Path,
			)
			// Brak wymaganego uprawnienia to 403 z error="insufficient_scope",
			// pozostałe przypadki to 401 z error="invalid_token" (RFC 6750).
			if errors.Is(err, ErrBrakScope) {
				odmow(w, o, http.StatusForbidden, "insufficient_scope", err)
				return
			}
			odmow(w, o, http.StatusUnauthorized, "invalid_token", err)
			return
		}

		ctx := context.WithValue(r.Context(), kluczRoszczen, roszczenia)
		nastepny.ServeHTTP(w, r.WithContext(ctx))
	})
}

// tokenZNaglowka wyciąga token z nagłówka Authorization.
//
// Token wolno przekazywać wyłącznie nagłówkiem — nigdy w query stringu, bo ten
// ląduje w logach serwerów pośredniczących.
func tokenZNaglowka(r *http.Request) (string, error) {
	naglowek := r.Header.Get("Authorization")
	if naglowek == "" {
		return "", ErrBrakTokenu
	}
	schemat, wartosc, ok := strings.Cut(naglowek, " ")
	if !ok || !strings.EqualFold(schemat, "Bearer") {
		return "", fmt.Errorf("%w: oczekuję nagłówka Authorization: Bearer <token>", ErrTokenNiepoprawny)
	}
	wartosc = strings.TrimSpace(wartosc)
	if wartosc == "" {
		return "", ErrBrakTokenu
	}
	return wartosc, nil
}

// odmow odpowiada błędem OAuth wraz z nagłówkiem WWW-Authenticate.
func odmow(w http.ResponseWriter, o Opcje, kod int, blad string, przyczyna error) {
	czesci := []string{`Bearer realm="systim-mcp"`}
	if o.URLMetadanych != "" {
		czesci = append(czesci, fmt.Sprintf(`resource_metadata=%q`, o.URLMetadanych))
	}
	if s := o.ogloszonyScope(); s != "" {
		czesci = append(czesci, fmt.Sprintf(`scope=%q`, s))
	}
	czesci = append(czesci, fmt.Sprintf(`error=%q`, blad))
	if przyczyna != nil {
		czesci = append(czesci, fmt.Sprintf(`error_description=%q`, oczyscOpis(przyczyna.Error())))
	}

	w.Header().Set("WWW-Authenticate", strings.Join(czesci, ", "))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(kod)
	json.NewEncoder(w).Encode(map[string]string{
		"error":             blad,
		"error_description": oczyscOpis(przyczynaPL(przyczyna)),
	})
}

// oczyscOpis usuwa z opisu znaki, które rozbiłyby nagłówek HTTP.
func oczyscOpis(s string) string {
	s = strings.ReplaceAll(s, `"`, "'")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// przyczynaPL zamienia błąd walidacji na komunikat po polsku.
func przyczynaPL(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBrakTokenu):
		return "Brak tokenu dostępowego. Dodaj nagłówek Authorization: Bearer <token>."
	case errors.Is(err, ErrTokenWygasl):
		return "Token dostępowy wygasł. Odśwież go przez refresh_token."
	case errors.Is(err, ErrZlaAudience):
		return "Token nie jest przeznaczony dla tego zasobu (niezgodne aud)."
	case errors.Is(err, ErrZlyIssuer):
		return "Token pochodzi od innego serwera autoryzacji (niezgodne iss)."
	case errors.Is(err, ErrBrakScope):
		return "Token nie ma uprawnienia wymaganego do wywołania narzędzi tego serwera."
	default:
		return "Token jest niepoprawny."
	}
}

// SprawdzOrigin odrzuca żądania z nieznanym nagłówkiem Origin.
//
// To wymóg specyfikacji MCP dla serwerów HTTP: ochrona przed atakiem DNS rebinding,
// w którym strona w przeglądarce ofiary wywołuje lokalny serwer MCP.
//
// Żądanie bez nagłówka Origin przechodzi — Origin ustawiają przeglądarki, a Claude
// łączy się z chmury Anthropic po stronie serwera i tego nagłówka nie wysyła.
// Odrzucanie takich żądań zablokowałoby konektor.
func SprawdzOrigin(nastepny http.Handler, dozwolone []string, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	zbior := make(map[string]bool, len(dozwolone))
	for _, o := range dozwolone {
		if o = strings.TrimRight(strings.TrimSpace(o), "/"); o != "" {
			zbior[strings.ToLower(o)] = true
		}
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
		if origin == "" {
			nastepny.ServeHTTP(w, r)
			return
		}
		// "null" wysyłają m.in. dokumenty z sandboxa i lokalne pliki — nigdy nie ufamy.
		if strings.EqualFold(origin, "null") || !zbior[strings.ToLower(origin)] {
			log.WarnContext(r.Context(), "odrzucono żądanie z nieznanym Origin",
				"origin", origin,
				"sciezka", r.URL.Path,
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{
				"error":             "forbidden_origin",
				"error_description": "Żądanie pochodzi z niedozwolonego Origin.",
			})
			return
		}
		nastepny.ServeHTTP(w, r)
	})
}
