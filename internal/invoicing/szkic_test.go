package invoicing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func podpisTestowy(t *testing.T, klucz string) *PodpisSzkicow {
	t.Helper()
	p, err := NowyPodpisSzkicow([]byte(klucz))
	if err != nil {
		t.Fatalf("NowyPodpisSzkicow = %v", err)
	}
	return p
}

func dokumentTestowy(t *testing.T) Dokument {
	t.Helper()
	pozycje, sumy, err := Oblicz([]PozycjaWejsciowa{
		{Opis: "Konsultacje", Ilosc: "10", CenaNetto: "200", StawkaVAT: "23", Jednostka: "h"},
	}, "", stawkiTestowe(t))
	if err != nil {
		t.Fatalf("Oblicz = %v", err)
	}
	return Dokument{
		IDKontrahenta:    "41",
		NazwaKontrahenta: "Kowalski & Synowie",
		DataWystawienia:  "2026-07-24",
		DataSprzedazy:    "2026-07-24",
		Rodzaj:           0,
		RodzajNazwa:      "faktura VAT",
		Pozycje:          pozycje,
		Podsumowanie:     sumy,
		TerminPlatnosci:  14,
		FormaPlatnosci:   "przelew",
	}
}

func TestSzkicPoprawnyPrzechodzi(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))
	dok := dokumentTestowy(t)

	szkicID, err := p.Podpisz(dok)
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	odczytany, err := p.Zweryfikuj(szkicID)
	if err != nil {
		t.Fatalf("Zweryfikuj = %v, chcę sukcesu", err)
	}

	// Szkic musi przenieść kwoty co do grosza — to na ich podstawie użytkownik zatwierdza dokument.
	if !odczytany.Podsumowanie.Brutto.Equal(dok.Podsumowanie.Brutto) {
		t.Errorf("brutto po odczycie = %s, chcę %s", odczytany.Podsumowanie.Brutto, dok.Podsumowanie.Brutto)
	}
	sprawdzKwote(t, "brutto", odczytany.Podsumowanie.Brutto, "2460.00")
	if odczytany.IDKontrahenta != "41" || odczytany.NazwaKontrahenta != "Kowalski & Synowie" {
		t.Errorf("dokument po odczycie = %+v", odczytany)
	}
	if len(odczytany.Pozycje) != 1 || odczytany.Pozycje[0].StawkaVATID != "1" {
		t.Errorf("pozycje po odczycie = %+v", odczytany.Pozycje)
	}
	if odczytany.TerminPlatnosci != 14 || odczytany.FormaPlatnosci != "przelew" {
		t.Errorf("pola opcjonalne zgubione: %+v", odczytany)
	}
}

func TestSzkicZeZmienionaKwotaJestOdrzucany(t *testing.T) {
	// Sedno rozdziału przygotuj/zatwierdź: użytkownik akceptuje konkretne kwoty,
	// więc kwoty nie mogą się zmienić między podglądem a zapisem.
	p := podpisTestowy(t, strings.Repeat("k", 32))
	szkicID, err := p.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}

	czescPayload, czescPodpis, _ := strings.Cut(szkicID, ".")
	payload, err := base64.RawURLEncoding.DecodeString(czescPayload)
	if err != nil {
		t.Fatalf("dekodowanie payloadu = %v", err)
	}

	// Podmieniamy kwotę brutto z 2460 na 1.00, zostawiając podpis bez zmian.
	zmieniony := strings.Replace(string(payload), `"2460"`, `"1"`, 1)
	if zmieniony == string(payload) {
		// Zapis kwoty zależy od formatu decimal — podmieniamy cokolwiek w payloadzie.
		zmieniony = strings.Replace(string(payload), `"200"`, `"1"`, 1)
	}
	if zmieniony == string(payload) {
		t.Fatalf("nie udało się zmodyfikować payloadu: %s", payload)
	}
	sfalszowany := base64.RawURLEncoding.EncodeToString([]byte(zmieniony)) + "." + czescPodpis

	_, err = p.Zweryfikuj(sfalszowany)
	if err == nil {
		t.Fatal("Zweryfikuj = nil, chcę odrzucenia szkicu ze zmienioną kwotą")
	}
	if !errors.Is(err, ErrSzkicNiepoprawny) {
		t.Errorf("err = %v, chcę ErrSzkicNiepoprawny", err)
	}
}

func TestSzkicZeZmienionymOdbiorcaJestOdrzucany(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))
	szkicID, err := p.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	czescPayload, czescPodpis, _ := strings.Cut(szkicID, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(czescPayload)

	zmieniony := strings.Replace(string(payload), `"id_kontrahenta":"41"`, `"id_kontrahenta":"99"`, 1)
	if zmieniony == string(payload) {
		t.Fatalf("nie udało się podmienić kontrahenta w: %s", payload)
	}
	sfalszowany := base64.RawURLEncoding.EncodeToString([]byte(zmieniony)) + "." + czescPodpis

	if _, err := p.Zweryfikuj(sfalszowany); !errors.Is(err, ErrSzkicNiepoprawny) {
		t.Errorf("err = %v, chcę ErrSzkicNiepoprawny", err)
	}
}

func TestSzkicPrzeterminowanyJestOdrzucanyZSensownymKomunikatem(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))

	// Szkic wystawiony 31 minut temu, przy TTL 30 minut.
	wydany := time.Now().Add(-31 * time.Minute)
	p.terazFn = func() time.Time { return wydany }
	szkicID, err := p.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}

	p.terazFn = time.Now
	_, err = p.Zweryfikuj(szkicID)
	if err == nil {
		t.Fatal("Zweryfikuj = nil, chcę odrzucenia szkicu po TTL")
	}
	if !errors.Is(err, ErrSzkicWygasl) {
		t.Errorf("err = %v, chcę ErrSzkicWygasl", err)
	}
	// Komunikat trafia do modelu — musi mówić, co zrobić dalej.
	if !strings.Contains(err.Error(), "przygotuj_fakture") {
		t.Errorf("komunikat %q nie podpowiada, żeby przygotować fakturę ponownie", err)
	}
}

func TestSzkicTuzPrzedWygasnieciemNadalDziala(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))
	wydany := time.Now().Add(-29 * time.Minute)
	p.terazFn = func() time.Time { return wydany }
	szkicID, err := p.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	p.terazFn = time.Now
	if _, err := p.Zweryfikuj(szkicID); err != nil {
		t.Errorf("Zweryfikuj = %v, chcę sukcesu 29 minut po wystawieniu (TTL to %s)", err, TTLSzkicu)
	}
}

func TestSzkicWygenerowanyInnymKluczemJestOdrzucany(t *testing.T) {
	// Chroni przed przyjęciem szkicu z innej instancji albo ze środowiska testowego.
	pA := podpisTestowy(t, strings.Repeat("a", 32))
	pB := podpisTestowy(t, strings.Repeat("b", 32))

	szkicID, err := pA.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	if _, err := pB.Zweryfikuj(szkicID); !errors.Is(err, ErrSzkicNiepoprawny) {
		t.Errorf("err = %v, chcę ErrSzkicNiepoprawny dla szkicu z obcym kluczem", err)
	}
	// Ten sam klucz w drugiej instancji ma działać — to jest warunek pracy stateless.
	pA2 := podpisTestowy(t, strings.Repeat("a", 32))
	if _, err := pA2.Zweryfikuj(szkicID); err != nil {
		t.Errorf("Zweryfikuj = %v; szkic musi się otwierać w innym procesie z tym samym kluczem", err)
	}
}

func TestSzkicNiepoprawnyFormat(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))
	przypadki := []struct {
		nazwa   string
		szkicID string
	}{
		{"pusty", ""},
		{"same spacje", "   "},
		{"bez kropki", "abcdef"},
		{"pusty payload", ".abcdef"},
		{"pusty podpis", "abcdef."},
		{"podpis nie-base64", "YWJj.@@@@"},
		{"payload nie-base64", "@@@@.YWJj"},
		{"przypadkowy tekst", "to nie jest szkic"},
	}
	for _, p2 := range przypadki {
		t.Run(p2.nazwa, func(t *testing.T) {
			_, err := p.Zweryfikuj(p2.szkicID)
			if err == nil {
				t.Fatal("Zweryfikuj = nil, chcę błędu")
			}
			if !errors.Is(err, ErrSzkicNiepoprawny) {
				t.Errorf("err = %v, chcę ErrSzkicNiepoprawny", err)
			}
		})
	}
}

func TestSzkicZeStaraWersjaJestOdrzucany(t *testing.T) {
	p := podpisTestowy(t, strings.Repeat("k", 32))
	s := Szkic{Wersja: 999, Wydany: time.Now().Unix(), Wygasa: time.Now().Add(time.Hour).Unix()}
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal = %v", err)
	}
	czescPayload := base64.RawURLEncoding.EncodeToString(payload)
	// Podpisujemy poprawnie — odrzucenie ma wynikać z wersji, nie z podpisu.
	szkicID := czescPayload + "." + base64.RawURLEncoding.EncodeToString(p.hmac([]byte(czescPayload)))

	_, err = p.Zweryfikuj(szkicID)
	if !errors.Is(err, ErrSzkicNiepoprawny) {
		t.Fatalf("err = %v, chcę ErrSzkicNiepoprawny", err)
	}
	if !strings.Contains(err.Error(), "wersji") {
		t.Errorf("komunikat %q nie wskazuje na niezgodność wersji", err)
	}
}

func TestKluczHMACMusiMiecMinimalnaDlugosc(t *testing.T) {
	if _, err := NowyPodpisSzkicow([]byte("za krótki")); err == nil {
		t.Error("krótki klucz został przyjęty, chcę błędu")
	}
	if _, err := NowyPodpisSzkicow([]byte(strings.Repeat("k", MinDlugoscKlucza-1))); err == nil {
		t.Errorf("klucz o długości %d został przyjęty", MinDlugoscKlucza-1)
	}
	if _, err := NowyPodpisSzkicow([]byte(strings.Repeat("k", MinDlugoscKlucza))); err != nil {
		t.Errorf("klucz o długości %d odrzucony: %v", MinDlugoscKlucza, err)
	}
}

func TestSzkicIDNieZawieraSurowychDanychWKluczuURL(t *testing.T) {
	// szkic_id bywa przekazywany dalej jako zwykły string — musi być bezpieczny
	// w URL-u i nie zawierać znaków wymagających dodatkowego kodowania.
	p := podpisTestowy(t, strings.Repeat("k", 32))
	szkicID, err := p.Podpisz(dokumentTestowy(t))
	if err != nil {
		t.Fatalf("Podpisz = %v", err)
	}
	for _, znak := range szkicID {
		czyOK := (znak >= 'A' && znak <= 'Z') || (znak >= 'a' && znak <= 'z') ||
			(znak >= '0' && znak <= '9') || znak == '-' || znak == '_' || znak == '.'
		if !czyOK {
			t.Fatalf("szkic_id zawiera znak %q spoza base64url", znak)
		}
	}
}
