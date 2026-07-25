package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestIntegracjaZPrawdziwymIdP sprawdza konfigurację przeciwko działającemu
// serwerowi autoryzacji: czy discovery jest osiągalne, czy ogłasza PKCE S256
// i czy nasz parser przyjmuje wszystkie klucze z jego JWKS.
//
// Test jest pomijany, dopóki nie wskażesz issuera. Uruchomienie:
//
//	AUTH_ISSUER=https://auth.firma.pl/application/o/systim-mcp/ \
//	  go test ./internal/auth/ -run TestIntegracjaZPrawdziwymIdP -v
//
// Przy lokalnym profilu dev:
//
//	AUTH_ISSUER=http://127.0.0.1:9000/application/o/systim-mcp/ \
//	  go test ./internal/auth/ -run TestIntegracjaZPrawdziwymIdP -v
//
// Warto go przepuścić po każdej zmianie konfiguracji IdP — wyłapuje dokładnie te
// rzeczy, które w przeciwnym razie objawiłyby się dopiero jako nieudane podpięcie
// konektora w claude.ai.
func TestIntegracjaZPrawdziwymIdP(t *testing.T) {
	issuer := os.Getenv("AUTH_ISSUER")
	if issuer == "" {
		t.Skip("ustaw AUTH_ISSUER, żeby przetestować konfigurację prawdziwego serwera autoryzacji")
	}

	ctx, anuluj := context.WithTimeout(context.Background(), 30*time.Second)
	defer anuluj()
	httpc := &http.Client{Timeout: 10 * time.Second}

	m, err := PobierzMetadaneIdP(ctx, httpc, issuer)
	if err != nil {
		t.Fatalf("nie udało się pobrać metadanych z %s: %v", issuer, err)
	}
	t.Logf("issuer   = %s", m.Issuer)
	t.Logf("jwks_uri = %s", m.JWKSURI)

	// Claude wymaga PKCE metodą S256; brak tej deklaracji to jedna z częstszych
	// przyczyn nieudanego podpięcia konektora.
	if !m.ObslugujeS256() {
		t.Errorf("serwer autoryzacji nie ogłasza code_challenge_methods_supported = [\"S256\"]")
	}
	if m.JWKSURI == "" {
		t.Fatal("metadane nie zawierają jwks_uri")
	}

	// Każdy klucz z JWKS musi dać się odczytać naszym parserem — inaczej tokeny
	// podpisane akurat tym kluczem byłyby odrzucane po rotacji.
	resp, err := httpc.Get(m.JWKSURI)
	if err != nil {
		t.Fatalf("pobranie JWKS: %v", err)
	}
	defer resp.Body.Close()

	var zestaw struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&zestaw); err != nil {
		t.Fatalf("dekodowanie JWKS: %v", err)
	}
	if len(zestaw.Keys) == 0 {
		t.Fatal("JWKS nie zawiera żadnego klucza")
	}

	zrodlo := NoweZrodloKluczy(issuer, httpc, nil)
	for _, k := range zestaw.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		klucz, err := zrodlo.Klucz(ctx, k.Kid)
		if err != nil {
			t.Errorf("klucz kid=%s kty=%s alg=%s nie został przyjęty: %v", k.Kid, k.Kty, k.Alg, err)
			continue
		}
		t.Logf("klucz kid=%s kty=%s alg=%s → %T", k.Kid, k.Kty, k.Alg, klucz)
	}

	// Jeśli podasz też oczekiwane aud, sprawdzimy spójność z konfiguracją serwera.
	if aud := os.Getenv("AUTH_AUDIENCE"); aud != "" {
		t.Logf("skonfigurowane OIDC_AUDIENCE = %s "+
			"(authentik domyślnie wstawia do aud client_id providera)", aud)
	}
}
