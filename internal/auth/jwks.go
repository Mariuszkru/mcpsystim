// Package auth realizuje rolę OAuth 2.1 resource servera: hostuje Protected
// Resource Metadata i waliduje tokeny dostępowe wydane przez zewnętrzny serwer
// autoryzacji.
//
// Świadomie nie ma tu żadnego kawałka serwera autoryzacji — tokenów nie wydajemy,
// tylko je sprawdzamy. Zgodna implementacja OAuth 2.1 to dużo pracy i łatwo o subtelny
// błąd bezpieczeństwa, więc wydawanie tokenów należy do gotowego dostawcy tożsamości.
package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// odswiezenieJWKS to okres, po którym zestaw kluczy jest uznawany za nieświeży.
	odswiezenieJWKS = 15 * time.Minute
	// minOdstepOdswiezen chroni serwer autoryzacji przed zalewem żądań, gdy
	// przychodzą tokeny z nieznanym kid (np. przy ataku albo błędnej konfiguracji).
	minOdstepOdswiezen = 1 * time.Minute
	// maxCialoJWKS ogranicza rozmiar pobieranych dokumentów.
	maxCialoJWKS = 1 << 20
)

// ErrNieznanyKlucz oznacza, że token wskazuje na kid spoza aktualnego zestawu.
var ErrNieznanyKlucz = errors.New("nieznany klucz podpisujący")

// ZrodloKluczy pobiera i cache'uje klucze publiczne serwera autoryzacji.
//
// Klucze IdP podlegają rotacji, więc zestaw jest odświeżany po upływie TTL, a także
// doraźnie, gdy pojawi się token podpisany kluczem, którego jeszcze nie znamy.
type ZrodloKluczy struct {
	issuer string
	httpc  *http.Client
	log    *slog.Logger

	mu            sync.RWMutex
	klucze        map[string]crypto.PublicKey
	jwksURI       string
	pobrane       time.Time
	ostatniaProba time.Time

	// odswiezMu serializuje pobieranie, żeby N równoległych żądań dało jedno pobranie.
	odswiezMu sync.Mutex
}

// NoweZrodloKluczy tworzy źródło kluczy dla podanego issuera.
func NoweZrodloKluczy(issuer string, httpc *http.Client, log *slog.Logger) *ZrodloKluczy {
	if httpc == nil {
		httpc = &http.Client{Timeout: 10 * time.Second}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ZrodloKluczy{
		issuer: strings.TrimRight(issuer, "/"),
		httpc:  httpc,
		log:    log,
		klucze: map[string]crypto.PublicKey{},
	}
}

// Klucz zwraca klucz publiczny o podanym kid.
//
// Gdy kid jest nieznany albo zestaw się zestarzał, klucze są pobierane ponownie —
// nie częściej jednak niż raz na minOdstepOdswiezen.
func (z *ZrodloKluczy) Klucz(ctx context.Context, kid string) (crypto.PublicKey, error) {
	z.mu.RLock()
	k, ok := z.klucze[kid]
	swieze := time.Since(z.pobrane) < odswiezenieJWKS
	z.mu.RUnlock()

	if ok && swieze {
		return k, nil
	}
	if err := z.odswiez(ctx, kid); err != nil {
		// Jeśli mamy klucz z poprzedniego pobrania, wolimy go użyć niż odrzucić
		// wszystkie żądania tylko dlatego, że IdP chwilowo nie odpowiada.
		if ok {
			z.log.WarnContext(ctx, "nie udało się odświeżyć JWKS, używam kluczy z cache", "blad", err)
			return k, nil
		}
		return nil, err
	}

	z.mu.RLock()
	k, ok = z.klucze[kid]
	z.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: kid %q nie występuje w zestawie kluczy serwera autoryzacji", ErrNieznanyKlucz, kid)
	}
	return k, nil
}

// odswiez pobiera zestaw kluczy tak, żeby kid dało się rozwiązać.
func (z *ZrodloKluczy) odswiez(ctx context.Context, kid string) error {
	z.odswiezMu.Lock()
	defer z.odswiezMu.Unlock()

	// Podwójne sprawdzenie: ktoś mógł odświeżyć zestaw, zanim weszliśmy do sekcji
	// krytycznej. Pytamy przy tym wprost o nasz kid, a nie o to, co wiedzieliśmy
	// przed wejściem.
	//
	// To rozróżnienie ma znaczenie przy zimnym starcie i przy rotacji kluczy, gdy
	// kilka żądań naraz trafia na nieznany kid: pierwsze pobiera JWKS, a pozostałe
	// czekają tutaj. Gdyby patrzeć na stan sprzed oczekiwania, wyglądałyby na takie,
	// które nadal nie mają klucza, i wpadałyby w limit częstotliwości poniżej —
	// czyli dostawałyby fałszywe 401 chwilę po tym, jak potrzebny klucz pojawił się
	// w cache. Stan czytamy pod jednym RLockiem, żeby nie zmienił się w międzyczasie.
	z.mu.RLock()
	_, mamyKlucz := z.klucze[kid]
	swieze := time.Since(z.pobrane) < odswiezenieJWKS
	odOstatniejProby := time.Since(z.ostatniaProba)
	pobraneNigdy := z.pobrane.IsZero()
	z.mu.RUnlock()

	if swieze && mamyKlucz {
		return nil
	}
	if odOstatniejProby < minOdstepOdswiezen && !pobraneNigdy {
		// Świeżo próbowaliśmy i się nie udało albo kid nadal nieznany — nie dobijamy IdP.
		return fmt.Errorf("%w: zestaw kluczy odświeżano mniej niż %s temu", ErrNieznanyKlucz, minOdstepOdswiezen)
	}

	z.mu.Lock()
	z.ostatniaProba = time.Now()
	z.mu.Unlock()

	uri, err := z.adresJWKS(ctx)
	if err != nil {
		return err
	}
	klucze, err := z.pobierzJWKS(ctx, uri)
	if err != nil {
		return err
	}

	z.mu.Lock()
	z.klucze = klucze
	z.pobrane = time.Now()
	z.mu.Unlock()

	z.log.InfoContext(ctx, "pobrano zestaw kluczy z serwera autoryzacji", "jwks_uri", uri, "liczba_kluczy", len(klucze))
	return nil
}

// metadaneIdP to fragment dokumentu discovery, który nas interesuje.
type metadaneIdP struct {
	Issuer                        string   `json:"issuer"`
	JWKSURI                       string   `json:"jwks_uri"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

// adresJWKS ustala adres zestawu kluczy z dokumentu discovery serwera autoryzacji.
func (z *ZrodloKluczy) adresJWKS(ctx context.Context) (string, error) {
	z.mu.RLock()
	uri := z.jwksURI
	z.mu.RUnlock()
	if uri != "" {
		return uri, nil
	}

	m, err := PobierzMetadaneIdP(ctx, z.httpc, z.issuer)
	if err != nil {
		return "", err
	}
	if m.JWKSURI == "" {
		return "", fmt.Errorf("serwer autoryzacji %s nie podał jwks_uri w metadanych", z.issuer)
	}

	z.mu.Lock()
	z.jwksURI = m.JWKSURI
	z.mu.Unlock()
	return m.JWKSURI, nil
}

// PobierzMetadaneIdP odczytuje dokument discovery serwera autoryzacji.
//
// Sprawdzamy przy okazji, czy IdP ogłasza PKCE metodą S256 — Claude tego wymaga,
// a brak tej deklaracji jest częstą przyczyną nieudanego podpięcia konektora.
func PobierzMetadaneIdP(ctx context.Context, httpc *http.Client, issuer string) (*metadaneIdP, error) {
	issuer = strings.TrimRight(issuer, "/")
	// Kolejność zgodna z praktyką: najpierw OIDC, potem czysty OAuth 2.0.
	adresy := []string{
		issuer + "/.well-known/openid-configuration",
		issuer + "/.well-known/oauth-authorization-server",
	}
	var ostatniBlad error
	for _, adres := range adresy {
		m, err := pobierzMetadane(ctx, httpc, adres)
		if err != nil {
			ostatniBlad = err
			continue
		}
		if m.Issuer != "" && strings.TrimRight(m.Issuer, "/") != issuer {
			return nil, fmt.Errorf("serwer autoryzacji ogłasza issuer %q, a skonfigurowano %q", m.Issuer, issuer)
		}
		return m, nil
	}
	return nil, fmt.Errorf("nie udało się pobrać metadanych serwera autoryzacji %s: %w", issuer, ostatniBlad)
}

func pobierzMetadane(ctx context.Context, httpc *http.Client, adres string) (*metadaneIdP, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adres, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pobranie %s: %w", adres, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pobranie %s: kod HTTP %d", adres, resp.StatusCode)
	}
	dane, err := io.ReadAll(io.LimitReader(resp.Body, maxCialoJWKS))
	if err != nil {
		return nil, fmt.Errorf("odczyt %s: %w", adres, err)
	}
	var m metadaneIdP
	if err := json.Unmarshal(dane, &m); err != nil {
		return nil, fmt.Errorf("dekodowanie %s: %w", adres, err)
	}
	return &m, nil
}

// ObslugujeS256 mówi, czy serwer autoryzacji ogłasza PKCE metodą S256.
func (m *metadaneIdP) ObslugujeS256() bool {
	for _, v := range m.CodeChallengeMethodsSupported {
		if v == "S256" {
			return true
		}
	}
	return false
}

// klucz JWK w postaci, w jakiej przychodzi z JWKS.
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// pobierzJWKS pobiera i parsuje zestaw kluczy publicznych.
func (z *ZrodloKluczy) pobierzJWKS(ctx context.Context, uri string) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := z.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pobranie JWKS z %s: %w", uri, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pobranie JWKS z %s: kod HTTP %d", uri, resp.StatusCode)
	}
	dane, err := io.ReadAll(io.LimitReader(resp.Body, maxCialoJWKS))
	if err != nil {
		return nil, fmt.Errorf("odczyt JWKS: %w", err)
	}

	var zestaw struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(dane, &zestaw); err != nil {
		return nil, fmt.Errorf("dekodowanie JWKS: %w", err)
	}

	klucze := make(map[string]crypto.PublicKey, len(zestaw.Keys))
	for _, k := range zestaw.Keys {
		// Klucze przeznaczone do szyfrowania nie służą do weryfikacji podpisu.
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		pub, err := k.naKluczPubliczny()
		if err != nil {
			z.log.WarnContext(ctx, "pomijam klucz z JWKS", "kid", k.Kid, "blad", err)
			continue
		}
		if k.Kid == "" {
			// Bez kid nie da się wskazać klucza z nagłówka tokenu.
			z.log.WarnContext(ctx, "pomijam klucz z JWKS bez pola kid")
			continue
		}
		klucze[k.Kid] = pub
	}
	if len(klucze) == 0 {
		return nil, fmt.Errorf("zestaw kluczy z %s nie zawiera żadnego użytecznego klucza", uri)
	}
	return klucze, nil
}

// naKluczPubliczny przekłada JWK na klucz publiczny Go.
func (k jwk) naKluczPubliczny() (crypto.PublicKey, error) {
	switch k.Kty {
	case "RSA":
		if k.N == "" || k.E == "" {
			return nil, errors.New("klucz RSA bez pól n/e")
		}
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("pole n: %w", err)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("pole e: %w", err)
		}
		wykladnik := new(big.Int).SetBytes(e)
		if !wykladnik.IsInt64() || wykladnik.Int64() > 1<<31-1 {
			return nil, errors.New("wykładnik publiczny poza zakresem")
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(wykladnik.Int64())}
		if pub.N.BitLen() < 2048 {
			return nil, fmt.Errorf("klucz RSA ma %d bitów, minimum to 2048", pub.N.BitLen())
		}
		return pub, nil

	case "EC":
		var krzywa elliptic.Curve
		switch k.Crv {
		case "P-256":
			krzywa = elliptic.P256()
		case "P-384":
			krzywa = elliptic.P384()
		case "P-521":
			krzywa = elliptic.P521()
		default:
			return nil, fmt.Errorf("nieobsługiwana krzywa %q", k.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(k.X)
		if err != nil {
			return nil, fmt.Errorf("pole x: %w", err)
		}
		y, err := base64.RawURLEncoding.DecodeString(k.Y)
		if err != nil {
			return nil, fmt.Errorf("pole y: %w", err)
		}
		pub := &ecdsa.PublicKey{Curve: krzywa, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !krzywa.IsOnCurve(pub.X, pub.Y) {
			return nil, errors.New("punkt nie leży na krzywej")
		}
		return pub, nil

	default:
		return nil, fmt.Errorf("nieobsługiwany typ klucza %q", k.Kty)
	}
}
