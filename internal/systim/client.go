package systim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxCialoOdpowiedzi ogranicza ilość danych czytanych z API. PDF-y faktur wracają
// w base64 wewnątrz JSON, więc limit musi być hojny, ale nie nieskończony.
const maxCialoOdpowiedzi = 64 << 20 // 64 MiB

// Client to klient API Systim. Jest bezpieczny do użycia z wielu goroutine.
//
// Token sesji trzymamy w pamięci pod RWMutex. Sesje API padają regularnie — wygasają
// po czasie z opcji konta, a każde zalogowanie użytkownika do panelu WWW kasuje
// wszystkie sesje API — więc automatyczne przelogowanie po błędzie 13 jest normalną
// ścieżką działania, nie obsługą sytuacji wyjątkowej.
type Client struct {
	baseURL string
	login   string
	pass    string
	httpc   *http.Client
	log     *slog.Logger

	mu    sync.RWMutex
	token string

	// loginMu serializuje logowanie. Gdy kilka narzędzi jednocześnie dostanie błąd 13,
	// logujemy się raz: pierwsza goroutine odświeża token, pozostałe po wejściu do
	// sekcji krytycznej widzą już nowy token i tylko go odczytują (podwójne sprawdzenie).
	loginMu sync.Mutex
}

// Opcje konfigurują klienta.
type Opcje struct {
	// Konto to poddomena konta w Systim (np. "abcd" dla abcd.systim.pl).
	Konto string
	Login string
	Pass  string
	// Timeout dotyczy pojedynczego wywołania HTTP do Systim.
	Timeout time.Duration
	Logger  *slog.Logger
	// BaseURL nadpisuje adres endpointu. Używane wyłącznie w testach.
	BaseURL string
}

// NewClient tworzy klienta API. Nie wykonuje logowania — pierwsze wywołanie
// zaloguje się samo.
func NewClient(o Opcje) (*Client, error) {
	if o.Login == "" || o.Pass == "" {
		return nil, errors.New("systim: login i hasło są wymagane")
	}
	base := o.BaseURL
	if base == "" {
		if o.Konto == "" {
			return nil, errors.New("systim: wymagana jest poddomena konta")
		}
		base = fmt.Sprintf("https://%s.systim.pl/jsonAPI", o.Konto)
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		baseURL: base,
		login:   o.Login,
		pass:    o.Pass,
		httpc:   &http.Client{Timeout: o.Timeout},
		log:     log,
	}, nil
}

// Wywolaj wykonuje metodę API i zwraca surowe pole "result".
//
// Przy błędzie 13 (brak sesji) klient loguje się ponownie i ponawia żądanie
// dokładnie raz. Drugi błąd 13 z rzędu jest zwracany do wywołującego — nie wchodzimy
// w pętlę przelogowań.
func (c *Client) Wywolaj(ctx context.Context, act string, params url.Values) (json.RawMessage, error) {
	token, err := c.token_(ctx)
	if err != nil {
		return nil, err
	}

	res, err := c.zadanie(ctx, act, params, token)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, ErrBrakSesji) {
		return nil, err
	}

	c.log.InfoContext(ctx, "sesja API wygasła, przelogowanie i ponowienie żądania", "act", act)
	nowy, oerr := c.odswiezToken(ctx, token)
	if oerr != nil {
		return nil, oerr
	}

	// Dokładnie jedno ponowienie. Jeśli i tu przyjdzie 13, zwracamy błąd.
	res, err = c.zadanie(ctx, act, params, nowy)
	if err != nil {
		if errors.Is(err, ErrBrakSesji) {
			c.log.WarnContext(ctx, "błąd 13 również po przelogowaniu, przerywam", "act", act)
		}
		return nil, err
	}
	return res, nil
}

// token_ zwraca aktualny token, logując się, jeśli jeszcze go nie ma.
func (c *Client) token_(ctx context.Context) (string, error) {
	c.mu.RLock()
	t := c.token
	c.mu.RUnlock()
	if t != "" {
		return t, nil
	}
	return c.odswiezToken(ctx, "")
}

// odswiezToken loguje się do API i zapisuje nowy token.
//
// stary to token, który wywołujący uznał za nieważny. Jeśli po wejściu do sekcji
// krytycznej okaże się, że bieżący token jest już inny, ktoś nas ubiegł i po prostu
// korzystamy z jego wyniku. Dzięki temu N równoległych błędów 13 daje jedno logowanie.
func (c *Client) odswiezToken(ctx context.Context, stary string) (string, error) {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	c.mu.RLock()
	biezacy := c.token
	c.mu.RUnlock()
	if biezacy != "" && biezacy != stary {
		return biezacy, nil
	}

	token, err := c.zaloguj(ctx)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return token, nil
}

// odpowiedzLogowania to kształt result metody login.
type odpowiedzLogowania struct {
	Token HTMLString `json:"token"`
}

// zaloguj wykonuje act=login i zwraca token sesji. Samo logowanie nie podlega
// ponowieniu po błędzie 13 — byłaby to pętla.
func (c *Client) zaloguj(ctx context.Context) (string, error) {
	params := url.Values{}
	params.Set("login", c.login)
	params.Set("pass", c.pass)

	raw, err := c.zadanie(ctx, "login", params, "")
	if err != nil {
		return "", fmt.Errorf("logowanie do Systim: %w", err)
	}
	var o odpowiedzLogowania
	if err := json.Unmarshal(raw, &o); err != nil {
		return "", fmt.Errorf("logowanie do Systim: nie umiem odczytać tokenu: %w", err)
	}
	if o.Token == "" {
		return "", errors.New("logowanie do Systim: API nie zwróciło tokenu")
	}
	c.log.InfoContext(ctx, "zalogowano do API Systim")
	return o.Token.String(), nil
}

// zadanie wykonuje pojedyncze wywołanie HTTP, bez logiki ponowień.
func (c *Client) zadanie(ctx context.Context, act string, params url.Values, token string) (json.RawMessage, error) {
	// Kopiujemy parametry, bo ta sama mapa jest używana ponownie przy retry
	// i nie chcemy jej mutować tokenem z poprzedniej próby.
	form := url.Values{}
	for k, vs := range params {
		for _, v := range vs {
			form.Add(k, v)
		}
	}
	form.Set("act", act)
	if token != "" {
		form.Set("token", token)
	}

	body := form.Encode()
	c.log.DebugContext(ctx, "wywołanie API Systim",
		"act", act,
		"params", zamaskujParametry(form),
		"rozmiar_ciala", len(body),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("budowa żądania do Systim: %w", err)
	}
	// API pracuje wyłącznie w UTF-8, a ciało jest form-urlencoded, nie JSON.
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("połączenie z Systim (metoda %s): %w", act, err)
	}
	defer resp.Body.Close()

	dane, err := io.ReadAll(io.LimitReader(resp.Body, maxCialoOdpowiedzi))
	if err != nil {
		return nil, fmt.Errorf("odczyt odpowiedzi Systim (metoda %s): %w", act, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Systim odpowiedziało kodem HTTP %d na metodę %s: %s",
			resp.StatusCode, act, skrot(dane))
	}

	var r Response
	if err := json.Unmarshal(dane, &r); err != nil {
		return nil, fmt.Errorf("dekodowanie odpowiedzi Systim (metoda %s): %w (treść: %s)",
			act, err, skrot(dane))
	}
	if r.Error.Code.Int() != KodOK {
		return nil, &SystimError{
			Code:    r.Error.Code.Int(),
			Message: r.Error.Message.String(),
			Fields:  r.Error.Fields,
			Act:     act,
		}
	}
	return r.Result, nil
}

// polaWrazliwe to nazwy parametrów, których wartości nigdy nie trafiają do logów.
var polaWrazliwe = map[string]bool{
	"pass":  true,
	"token": true,
	"login": true,
}

// zamaskujParametry zwraca kopię parametrów z zamaskowanymi wartościami pól
// wrażliwych, gotową do zalogowania.
func zamaskujParametry(form url.Values) string {
	czesci := make([]string, 0, len(form))
	for k := range form {
		if polaWrazliwe[k] {
			czesci = append(czesci, k+"=***")
			continue
		}
		czesci = append(czesci, k+"="+form.Get(k))
	}
	// Kolejność map w Go jest losowa; log jest diagnostyczny, więc to akceptowalne,
	// ale sortujemy dla czytelności przy porównywaniu wpisów.
	sortStringy(czesci)
	return strings.Join(czesci, "&")
}

func sortStringy(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// UstawTokenDoTestow pozwala testom wstrzyknąć token bez wywoływania logowania.
func (c *Client) UstawTokenDoTestow(t string) {
	c.mu.Lock()
	c.token = t
	c.mu.Unlock()
}
