package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strings"
	"time"
)

// tolerancjaZegara dopuszcza niewielki rozjazd zegarów między IdP a tym serwerem.
const tolerancjaZegara = 60 * time.Second

// Błędy walidacji tokenu. Rozdzielone, bo trafiają do nagłówka WWW-Authenticate
// i do logów — administrator musi wiedzieć, czy problem jest w audience, czasie
// ważności, czy w podpisie.
var (
	ErrBrakTokenu       = errors.New("brak tokenu dostępowego")
	ErrTokenNiepoprawny = errors.New("token jest niepoprawny")
	ErrTokenWygasl      = errors.New("token wygasł")
	ErrZlyIssuer        = errors.New("token pochodzi od innego serwera autoryzacji")
	ErrZlaAudience      = errors.New("token nie jest przeznaczony dla tego zasobu")
	ErrBrakScope        = errors.New("token nie ma wymaganego uprawnienia")
)

// Roszczenia to zestaw pól tokenu, które sprawdzamy.
type Roszczenia struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Audience  ElastycznaLista `json:"aud"`
	Expires   int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
	IssuedAt  int64           `json:"iat"`
	// Scope w formacie OAuth 2 — lista rozdzielona spacjami.
	Scope string `json:"scope"`
	// Scp bywa używane zamiennie przez część dostawców (tablica).
	Scp      ElastycznaLista `json:"scp"`
	ClientID string          `json:"client_id"`
	Azp      string          `json:"azp"`
}

// ElastycznaLista znosi pole, które w JWT bywa stringiem albo tablicą stringów —
// tak jest zdefiniowane "aud" w RFC 7519.
type ElastycznaLista []string

// UnmarshalJSON przyjmuje string i tablicę.
func (e *ElastycznaLista) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*e = nil
		return nil
	}
	if data[0] == '[' {
		var v []string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*e = v
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*e = []string{s}
	return nil
}

// Zawiera mówi, czy lista zawiera podaną wartość.
func (e ElastycznaLista) Zawiera(v string) bool {
	for _, x := range e {
		if x == v {
			return true
		}
	}
	return false
}

// Uprawnienia zwraca scope'y tokenu, niezależnie od tego, czy IdP użył pola
// "scope" (string ze spacjami), czy "scp" (tablica).
func (r *Roszczenia) Uprawnienia() []string {
	if s := strings.TrimSpace(r.Scope); s != "" {
		return strings.Fields(s)
	}
	return r.Scp
}

// MaScope sprawdza obecność konkretnego uprawnienia.
func (r *Roszczenia) MaScope(scope string) bool {
	for _, s := range r.Uprawnienia() {
		if s == scope {
			return true
		}
	}
	return false
}

// Walidator sprawdza tokeny dostępowe wydane przez skonfigurowany serwer autoryzacji.
type Walidator struct {
	klucze        *ZrodloKluczy
	issuer        string
	audience      string
	wymaganyScope string
	terazFn       func() time.Time
}

// NowyWalidator tworzy walidator tokenów.
func NowyWalidator(klucze *ZrodloKluczy, issuer, audience, wymaganyScope string) *Walidator {
	return &Walidator{
		klucze:        klucze,
		issuer:        strings.TrimRight(issuer, "/"),
		audience:      audience,
		wymaganyScope: wymaganyScope,
		terazFn:       time.Now,
	}
}

func (w *Walidator) teraz() time.Time {
	if w.terazFn != nil {
		return w.terazFn()
	}
	return time.Now()
}

// obslugiwaneAlg to jedyne akceptowane algorytmy podpisu. Lista jest zamknięta —
// w szczególności nie ma tu "none" ani algorytmów HMAC, których użycie z kluczem
// publicznym jest klasycznym atakiem na weryfikację JWT.
var obslugiwaneAlg = map[string]bool{
	"RS256": true, "RS384": true, "RS512": true,
	"PS256": true, "PS384": true, "PS512": true,
	"ES256": true, "ES384": true, "ES512": true,
}

// naglowekJWT to nagłówek tokenu.
type naglowekJWT struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Zweryfikuj sprawdza podpis i wszystkie wymagane roszczenia tokenu.
func (w *Walidator) Zweryfikuj(ctx context.Context, token string) (*Roszczenia, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrBrakTokenu
	}
	czesci := strings.Split(token, ".")
	if len(czesci) != 3 {
		return nil, fmt.Errorf("%w: oczekuję trzech części JWT, mam %d", ErrTokenNiepoprawny, len(czesci))
	}

	surowyNaglowek, err := base64.RawURLEncoding.DecodeString(czesci[0])
	if err != nil {
		return nil, fmt.Errorf("%w: nagłówka nie da się odczytać", ErrTokenNiepoprawny)
	}
	var n naglowekJWT
	if err := json.Unmarshal(surowyNaglowek, &n); err != nil {
		return nil, fmt.Errorf("%w: nagłówek nie jest poprawnym JSON-em", ErrTokenNiepoprawny)
	}
	if !obslugiwaneAlg[n.Alg] {
		return nil, fmt.Errorf("%w: algorytm podpisu %q nie jest akceptowany", ErrTokenNiepoprawny, n.Alg)
	}
	if n.Kid == "" {
		return nil, fmt.Errorf("%w: nagłówek nie wskazuje klucza (brak kid)", ErrTokenNiepoprawny)
	}

	podpis, err := base64.RawURLEncoding.DecodeString(czesci[2])
	if err != nil {
		return nil, fmt.Errorf("%w: podpisu nie da się odczytać", ErrTokenNiepoprawny)
	}

	klucz, err := w.klucze.Klucz(ctx, n.Kid)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTokenNiepoprawny, err)
	}

	podpisywane := []byte(czesci[0] + "." + czesci[1])
	if err := sprawdzPodpis(n.Alg, klucz, podpisywane, podpis); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTokenNiepoprawny, err)
	}

	// Roszczenia odczytujemy dopiero po potwierdzeniu podpisu.
	surowePayload, err := base64.RawURLEncoding.DecodeString(czesci[1])
	if err != nil {
		return nil, fmt.Errorf("%w: zawartości nie da się odczytać", ErrTokenNiepoprawny)
	}
	var r Roszczenia
	if err := json.Unmarshal(surowePayload, &r); err != nil {
		return nil, fmt.Errorf("%w: zawartość nie jest poprawnym JSON-em", ErrTokenNiepoprawny)
	}

	if err := w.sprawdzRoszczenia(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// sprawdzRoszczenia waliduje iss, aud, exp, nbf oraz wymagany scope.
func (w *Walidator) sprawdzRoszczenia(r *Roszczenia) error {
	if strings.TrimRight(r.Issuer, "/") != w.issuer {
		return fmt.Errorf("%w: iss = %q, oczekuję %q", ErrZlyIssuer, r.Issuer, w.issuer)
	}
	if !r.Audience.Zawiera(w.audience) {
		return fmt.Errorf("%w: aud = %v, oczekuję %q", ErrZlaAudience, []string(r.Audience), w.audience)
	}

	teraz := w.teraz()
	if r.Expires == 0 {
		return fmt.Errorf("%w: token nie ma pola exp", ErrTokenNiepoprawny)
	}
	if teraz.After(time.Unix(r.Expires, 0).Add(tolerancjaZegara)) {
		return fmt.Errorf("%w: wygasł %s", ErrTokenWygasl, time.Unix(r.Expires, 0).UTC().Format(time.RFC3339))
	}
	if r.NotBefore != 0 && teraz.Before(time.Unix(r.NotBefore, 0).Add(-tolerancjaZegara)) {
		return fmt.Errorf("%w: jeszcze nie obowiązuje (nbf = %s)", ErrTokenNiepoprawny,
			time.Unix(r.NotBefore, 0).UTC().Format(time.RFC3339))
	}

	if w.wymaganyScope != "" && !r.MaScope(w.wymaganyScope) {
		return fmt.Errorf("%w: brakuje scope %q; token ma: %v", ErrBrakScope, w.wymaganyScope, r.Uprawnienia())
	}
	return nil
}

// sprawdzPodpis weryfikuje podpis odpowiednim algorytmem, pilnując, żeby typ
// klucza zgadzał się z zadeklarowanym algorytmem.
func sprawdzPodpis(alg string, klucz crypto.PublicKey, dane, podpis []byte) error {
	h, hashID, err := skrotDlaAlg(alg)
	if err != nil {
		return err
	}
	h.Write(dane)
	suma := h.Sum(nil)

	switch {
	case strings.HasPrefix(alg, "RS"):
		pub, ok := klucz.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("algorytm %s wymaga klucza RSA, a klucz jest innego typu", alg)
		}
		return rsa.VerifyPKCS1v15(pub, hashID, suma, podpis)

	case strings.HasPrefix(alg, "PS"):
		pub, ok := klucz.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("algorytm %s wymaga klucza RSA, a klucz jest innego typu", alg)
		}
		return rsa.VerifyPSS(pub, hashID, suma, podpis, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthAuto})

	case strings.HasPrefix(alg, "ES"):
		pub, ok := klucz.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("algorytm %s wymaga klucza EC, a klucz jest innego typu", alg)
		}
		// W JWS podpis ECDSA to konkatenacja r i s o stałej długości, nie DER.
		rozmiar := (pub.Curve.Params().BitSize + 7) / 8
		if len(podpis) != 2*rozmiar {
			return fmt.Errorf("podpis ECDSA ma %d bajtów, oczekuję %d", len(podpis), 2*rozmiar)
		}
		rr := new(big.Int).SetBytes(podpis[:rozmiar])
		ss := new(big.Int).SetBytes(podpis[rozmiar:])
		if !ecdsa.Verify(pub, suma, rr, ss) {
			return errors.New("podpis ECDSA się nie zgadza")
		}
		return nil

	default:
		return fmt.Errorf("nieobsługiwany algorytm %q", alg)
	}
}

// skrotDlaAlg zwraca funkcję skrótu odpowiadającą algorytmowi podpisu.
func skrotDlaAlg(alg string) (hash.Hash, crypto.Hash, error) {
	switch alg[2:] {
	case "256":
		return sha256.New(), crypto.SHA256, nil
	case "384":
		return sha512.New384(), crypto.SHA384, nil
	case "512":
		return sha512.New(), crypto.SHA512, nil
	default:
		return nil, 0, fmt.Errorf("nieobsługiwany algorytm %q", alg)
	}
}
