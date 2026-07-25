// Command systim-mcp uruchamia serwer MCP integrujący Claude z systemem
// fakturowym Systim.
//
// Docelowy tryb pracy to serwer zdalny na Streamable HTTP, podpinany do claude.ai
// jako custom connector. Transport stdio służy wyłącznie do lokalnego debugowania.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mariuszkru/mcpsystim/internal/auth"
	"github.com/Mariuszkru/mcpsystim/internal/config"
	"github.com/Mariuszkru/mcpsystim/internal/invoicing"
	"github.com/Mariuszkru/mcpsystim/internal/systim"
	"github.com/Mariuszkru/mcpsystim/internal/tools"
)

// wersja jest nadpisywana przy budowaniu przez -ldflags "-X main.wersja=...".
var wersja = "dev"

const (
	// czasNaZamkniecie to czas, jaki dajemy trwającym żądaniom przy wyłączaniu.
	czasNaZamkniecie = 20 * time.Second
	// timeoutOdczytuNaglowkow chroni przed powolnymi klientami trzymającymi połączenia.
	timeoutOdczytuNaglowkow = 10 * time.Second
	timeoutOdczytu          = 60 * time.Second
	timeoutZapisu           = 5 * time.Minute
	timeoutBezczynnosci     = 120 * time.Second
)

func main() {
	// Obraz distroless nie ma powłoki ani curl-a, więc sondę HEALTHCHECK
	// wykonuje sam plik binarny wywołany z tą flagą.
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		if err := sonda(); err != nil {
			fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := uruchom(); err != nil {
		// Konfiguracja jest walidowana przed zbudowaniem loggera, więc komunikat
		// o jej błędzie musi trafić na stderr wprost.
		fmt.Fprintf(os.Stderr, "systim-mcp: %v\n", err)
		os.Exit(1)
	}
}

// sonda odpytuje /healthz lokalnego procesu. Zwraca błąd, gdy serwer nie odpowiada
// albo odpowiada innym kodem niż 200.
func sonda() error {
	adres := os.Getenv("SYSTIM_ADDR")
	if adres == "" {
		adres = config.DomyslnyAdres
	}
	// SYSTIM_ADDR ma postać ":8000" albo "0.0.0.0:8000"; sondujemy pętlę zwrotną.
	_, port, err := net.SplitHostPort(adres)
	if err != nil {
		return fmt.Errorf("nie umiem odczytać portu z SYSTIM_ADDR=%q: %w", adres, err)
	}

	klient := &http.Client{Timeout: 4 * time.Second}
	resp, err := klient.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/healthz odpowiedziało kodem %d", resp.StatusCode)
	}
	return nil
}

func uruchom() error {
	cfg, err := config.Wczytaj()
	if err != nil {
		return err
	}

	log := zbudujLogger(cfg)
	slog.SetDefault(log)

	klient, err := systim.NewClient(systim.Opcje{
		Konto:   cfg.Konto,
		Login:   cfg.Login,
		Pass:    cfg.Pass,
		Timeout: cfg.Timeout,
		Logger:  log,
	})
	if err != nil {
		return err
	}
	stawki, err := invoicing.NoweStawkiVAT(cfg.VatIDs)
	if err != nil {
		return err
	}
	szkice, err := invoicing.NowyPodpisSzkicow(cfg.SzkicKlucz)
	if err != nil {
		return err
	}

	serwerNarzedzi := tools.NowySerwer(klient, stawki, szkice, cfg, log)
	mcpSerwer := serwerNarzedzi.Zarejestruj(&mcp.Implementation{
		Name:    "systim-mcp",
		Title:   "Systim — faktury sprzedaży",
		Version: wersja,
	}, &mcp.ServerOptions{
		Instructions: "Serwer wystawia faktury sprzedaży w systemie Systim. " +
			"Wystawienie dokumentu jest dwuetapowe: przygotuj_fakture liczy kwoty i zwraca podgląd, " +
			"a dopiero zatwierdz_fakture zapisuje dokument w Systim. " +
			"Zawsze pokaż użytkownikowi kwoty z podglądu i uzyskaj jego wyraźną zgodę przed " +
			"zatwierdzeniem — operacja jest nieodwracalna.",
	})

	// Sygnały przerywają context, który propaguje się do trwających żądań.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("start serwera systim-mcp",
		"wersja", wersja,
		"transport", string(cfg.Transport),
		"konto", cfg.Konto,
		"katalog_pdf", cfg.KatalogPDF,
		"stawki_vat", stawki.Dostepne(),
	)

	switch cfg.Transport {
	case config.TransportStdio:
		return uruchomStdio(ctx, mcpSerwer, log)
	default:
		return uruchomHTTP(ctx, mcpSerwer, cfg, log)
	}
}

// zbudujLogger tworzy logger JSON na stdout, a przy transporcie stdio na stderr.
//
// Przy stdio strumień stdout należy wyłącznie do protokołu MCP — cokolwiek innego
// tam trafi, rozsypie sesję.
func zbudujLogger(cfg *config.Config) *slog.Logger {
	wyjscie := os.Stdout
	if cfg.Transport == config.TransportStdio {
		wyjscie = os.Stderr
	}
	return slog.New(slog.NewJSONHandler(wyjscie, &slog.HandlerOptions{Level: cfg.LogLevel}))
}

// uruchomStdio obsługuje pojedynczą sesję na stdin/stdout.
func uruchomStdio(ctx context.Context, srv *mcp.Server, log *slog.Logger) error {
	log.Info("nasłuch na stdio; logi idą na stderr, stdout jest zarezerwowany dla protokołu MCP")
	if err := srv.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("sesja stdio: %w", err)
	}
	log.Info("sesja stdio zakończona")
	return nil
}

// zbudujMux składa routing serwera HTTP: endpoint MCP za warstwami ochronnymi,
// Protected Resource Metadata oraz sondę /healthz.
//
// Wydzielone z uruchomHTTP, żeby dało się przetestować całe okablowanie bez
// otwierania gniazda i obsługi sygnałów.
func zbudujMux(srv *mcp.Server, cfg *config.Config, walidator *auth.Walidator, log *slog.Logger) http.Handler {
	// Tryb stateless jest wymagany przez najnowszą wersję protokołu w tym transporcie:
	// żądania w tej wersji kierowane do handlera niestateless są odrzucane.
	// Serwer nie trzyma więc stanu per-sesja — szkice faktur są samowystarczalnymi,
	// podpisanymi tokenami, więc działają także po restarcie i przy wielu replikach.
	handlerMCP := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			JSONResponse:                 true,
			Logger:                       log,
			MaxRequestBodyBytes:          cfg.MaxCialo,
			PropagateRequestCancellation: true,
			DisableLocalhostProtection:   cfg.WylaczOchroneLocalhost,
		},
	)

	// Kolejność warstw: najpierw Origin (tanie odrzucenie, bez sięgania po klucze
	// IdP), dopiero potem walidacja tokenu.
	chroniony := auth.Wymagaj(handlerMCP, auth.Opcje{
		Walidator:     walidator,
		URLMetadanych: cfg.URLMetadanychZasobu(),
		Zasob:         cfg.URLZasobu(),
		WymaganyScope: cfg.OIDCScope,
		ZadaneScopes:  cfg.OIDCScopesZadane,
		Logger:        log,
	})
	chroniony = auth.SprawdzOrigin(chroniony, cfg.DozwoloneOriginy(), log)

	metadane := auth.HandlerMetadanych(auth.MetadaneZasobu{
		Resource:             cfg.URLZasobu(),
		AuthorizationServers: []string{cfg.OIDCIssuer},
		ScopesSupported:      cfg.OIDCScopesZadane,
		// Token wolno przekazywać wyłącznie nagłówkiem Authorization —
		// w query stringu trafiłby do logów serwerów pośredniczących.
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "Systim MCP — faktury sprzedaży",
	})

	mux := http.NewServeMux()
	mux.Handle("/mcp", chroniony)
	// RFC 9728 dopuszcza wariant ze ścieżką zasobu w adresie metadanych.
	// Serwujemy oba, bo klienci pytają raz o jeden, raz o drugi.
	mux.Handle("/.well-known/oauth-protected-resource", metadane)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", metadane)
	// Sonda musi zostać poza warstwą autoryzacji — odpytuje ją HEALTHCHECK
	// kontenera i platforma hostingowa, które nie mają tokenu.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","wersja":%q}`, wersja)
	})
	return mux
}

// uruchomHTTP startuje serwer Streamable HTTP wraz z endpointami pomocniczymi.
func uruchomHTTP(ctx context.Context, srv *mcp.Server, cfg *config.Config, log *slog.Logger) error {
	walidator, err := zbudujWalidator(ctx, cfg, log)
	if err != nil {
		return err
	}

	// ctxZadan żyje dłużej niż context sygnału: gdy przyjdzie SIGTERM, trwające
	// żądania mają dokończyć pracę, a nie zostać ucięte w pół wywołania do Systim.
	// Anulujemy go dopiero po zakończeniu (albo po przekroczeniu limitu) Shutdown.
	ctxZadan, przerwijZadania := context.WithCancel(context.Background())
	defer przerwijZadania()

	serwer := &http.Server{
		Addr:              cfg.Adres,
		Handler:           zbudujMux(srv, cfg, walidator, log),
		ReadHeaderTimeout: timeoutOdczytuNaglowkow,
		ReadTimeout:       timeoutOdczytu,
		WriteTimeout:      timeoutZapisu,
		IdleTimeout:       timeoutBezczynnosci,
		BaseContext:       func(net.Listener) context.Context { return ctxZadan },
	}

	if cfg.AuthDisabled {
		ostrzezenieOWylaczonejAutoryzacji(log, cfg)
	}
	log.Info("nasłuch HTTP",
		"adres", cfg.Adres,
		"endpoint_mcp", cfg.URLZasobu(),
		"metadane_zasobu", cfg.URLMetadanychZasobu(),
		"dozwolone_originy", cfg.DozwoloneOriginy(),
		"autoryzacja", !cfg.AuthDisabled,
		"stateless", true,
	)

	bledy := make(chan error, 1)
	go func() {
		if err := serwer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			bledy <- err
		}
	}()

	select {
	case err := <-bledy:
		return fmt.Errorf("serwer HTTP: %w", err)
	case <-ctx.Done():
		log.Info("otrzymano sygnał zatrzymania, kończę obsługę trwających żądań",
			"limit", czasNaZamkniecie.String())
	}

	// Shutdown dostaje własny context — ten z sygnału jest już anulowany.
	// Shutdown przestaje przyjmować nowe połączenia i czeka na dokończenie
	// trwających żądań; dopiero gdy się nie wyrobią, ucinamy je przez ctxZadan.
	ctxZamkniecia, anuluj := context.WithTimeout(context.Background(), czasNaZamkniecie)
	defer anuluj()

	err = serwer.Shutdown(ctxZamkniecia)
	przerwijZadania()
	if err != nil {
		return fmt.Errorf("zamykanie serwera HTTP przekroczyło limit %s: %w", czasNaZamkniecie, err)
	}
	log.Info("serwer zatrzymany, wszystkie żądania dokończone")
	return nil
}

// zbudujWalidator tworzy walidator tokenów albo zwraca nil, gdy autoryzacja
// jest świadomie wyłączona.
func zbudujWalidator(ctx context.Context, cfg *config.Config, log *slog.Logger) (*auth.Walidator, error) {
	if cfg.AuthDisabled {
		return nil, nil
	}

	httpc := &http.Client{Timeout: 10 * time.Second}
	klucze := auth.NoweZrodloKluczy(cfg.OIDCIssuer, httpc, log)

	// Sprawdzenie przy starcie jest miękkie: IdP w docker compose potrafi wstawać
	// wolniej niż ten kontener, a klucze i tak pobierzemy przy pierwszym żądaniu.
	ctxSprawdzenia, anuluj := context.WithTimeout(ctx, 10*time.Second)
	defer anuluj()

	m, err := auth.PobierzMetadaneIdP(ctxSprawdzenia, httpc, cfg.OIDCIssuer)
	switch {
	case err != nil:
		log.Warn("nie udało się pobrać metadanych serwera autoryzacji przy starcie; "+
			"spróbuję ponownie przy pierwszym żądaniu",
			"issuer", cfg.OIDCIssuer, "blad", err.Error())
	case !m.ObslugujeS256():
		log.Warn("serwer autoryzacji nie ogłasza code_challenge_methods_supported = [\"S256\"]. "+
			"Claude wymaga PKCE metodą S256 — podpięcie konektora najpewniej się nie powiedzie",
			"issuer", cfg.OIDCIssuer)
	default:
		log.Info("serwer autoryzacji odnaleziony",
			"issuer", cfg.OIDCIssuer, "jwks_uri", m.JWKSURI, "pkce_s256", true)
	}

	return auth.NowyWalidator(klucze, cfg.OIDCIssuer, cfg.OIDCAudience, cfg.OIDCScope), nil
}

// ostrzezenieOWylaczonejAutoryzacji wypisuje głośne ostrzeżenie przy każdym starcie.
//
// Serwer daje pełny dostęp do księgowości firmy, a token API Systim nie podlega
// uprawnieniom — jeden token to wszystkie operacje na koncie, łącznie z usuwaniem
// danych. Otwarty endpoint HTTP jest więc realnym zagrożeniem, nie formalnością.
func ostrzezenieOWylaczonejAutoryzacji(log *slog.Logger, cfg *config.Config) {
	log.Warn("UWAGA: WALIDACJA TOKENU JEST WYŁĄCZONA (SYSTIM_AUTH_DISABLED=true). " +
		"Endpoint /mcp jest dostępny bez uwierzytelnienia, a serwer daje pełny dostęp " +
		"do księgowości firmy — API Systim nie ma granularnych uprawnień. " +
		"Ten tryb jest przeznaczony WYŁĄCZNIE do testów lokalnych i nie wolno go wystawiać " +
		"na publiczny adres.")
	log.Warn("wyłączona autoryzacja — konfiguracja", "adres", cfg.Adres, "publiczny_url", cfg.PublicURL)
}
