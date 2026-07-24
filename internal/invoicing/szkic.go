package invoicing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TTLSzkicu to czas życia szkicu faktury.
const TTLSzkicu = 30 * time.Minute

// MinDlugoscKlucza to minimalna długość klucza HMAC.
const MinDlugoscKlucza = 32

// wersjaSzkicu pozwala odrzucić szkice w starym formacie po zmianie struktury.
const wersjaSzkicu = 1

// Błędy weryfikacji szkicu. Rozdzielone, bo użytkownik potrzebuje różnych podpowiedzi:
// szkic wygasły trzeba przygotować ponownie, a szkic z niepoprawnym podpisem
// oznacza, że ktoś go zmodyfikował albo pochodzi z innego serwera.
var (
	ErrSzkicNiepoprawny = errors.New("szkic faktury jest niepoprawny")
	ErrSzkicWygasl      = errors.New("szkic faktury wygasł")
)

// Szkic to komplet danych dokumentu policzony przez przygotuj_fakture.
//
// Szkic jest przenoszony w całości wewnątrz szkic_id, a nie trzymany w pamięci
// procesu. Serwer działa w trybie stateless: kolejne żądanie może trafić do innego
// procesu albo po restarcie, a szkic i tak musi się otworzyć.
type Szkic struct {
	Wersja   int      `json:"v"`
	Wydany   int64    `json:"iat"`
	Wygasa   int64    `json:"exp"`
	Dokument Dokument `json:"dok"`
}

// Dokument to dane dokumentu gotowe do wysłania do Systim.
type Dokument struct {
	IDKontrahenta    string `json:"id_kontrahenta"`
	NazwaKontrahenta string `json:"nazwa_kontrahenta,omitempty"`
	DataWystawienia  string `json:"data_wystawienia"`
	DataSprzedazy    string `json:"data_sprzedazy"`
	Rodzaj           int    `json:"rodzaj"`
	RodzajNazwa      string `json:"rodzaj_nazwa,omitempty"`

	Pozycje      []PozycjaObliczona `json:"pozycje"`
	Podsumowanie Podsumowanie       `json:"podsumowanie"`

	TerminPlatnosci int    `json:"termin_platnosci,omitempty"`
	FormaPlatnosci  string `json:"forma_platnosci,omitempty"`
	Uwagi           string `json:"uwagi,omitempty"`
	Rabat           string `json:"rabat,omitempty"`
	WyslijDoKSeF    bool   `json:"wyslij_do_ksef,omitempty"`
}

// PodpisSzkicow podpisuje i weryfikuje szkice faktur.
type PodpisSzkicow struct {
	klucz []byte
	// terazFn pozwala testom sterować upływem czasu.
	terazFn func() time.Time
}

// NowyPodpisSzkicow tworzy podpisywacz na podstawie klucza HMAC z konfiguracji.
func NowyPodpisSzkicow(klucz []byte) (*PodpisSzkicow, error) {
	if len(klucz) < MinDlugoscKlucza {
		return nil, fmt.Errorf("klucz do podpisywania szkiców ma %d bajtów, wymagane minimum to %d "+
			"(zmienna SYSTIM_SZKIC_KLUCZ)", len(klucz), MinDlugoscKlucza)
	}
	kopia := make([]byte, len(klucz))
	copy(kopia, klucz)
	return &PodpisSzkicow{klucz: kopia, terazFn: time.Now}, nil
}

// teraz zwraca bieżący czas, z możliwością nadpisania w testach.
func (p *PodpisSzkicow) teraz() time.Time {
	if p.terazFn != nil {
		return p.terazFn()
	}
	return time.Now()
}

// Podpisz pakuje dokument w samowystarczalny, podpisany szkic_id.
//
// Format: base64url(payload JSON) + "." + base64url(HMAC-SHA256(payload)).
func (p *PodpisSzkicow) Podpisz(dok Dokument) (string, error) {
	teraz := p.teraz()
	s := Szkic{
		Wersja:   wersjaSzkicu,
		Wydany:   teraz.Unix(),
		Wygasa:   teraz.Add(TTLSzkicu).Unix(),
		Dokument: dok,
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("serializacja szkicu: %w", err)
	}
	czescPayload := base64.RawURLEncoding.EncodeToString(payload)
	podpis := p.hmac([]byte(czescPayload))
	return czescPayload + "." + base64.RawURLEncoding.EncodeToString(podpis), nil
}

// Zweryfikuj sprawdza podpis i termin ważności szkicu, po czym zwraca dokument.
//
// Podpis jest sprawdzany przed odczytaniem zawartości — nie ufamy danym, których
// autentyczności jeszcze nie potwierdziliśmy.
func (p *PodpisSzkicow) Zweryfikuj(szkicID string) (Dokument, error) {
	szkicID = strings.TrimSpace(szkicID)
	if szkicID == "" {
		return Dokument{}, fmt.Errorf("%w: nie podano szkic_id", ErrSzkicNiepoprawny)
	}
	czescPayload, czescPodpis, ok := strings.Cut(szkicID, ".")
	if !ok || czescPayload == "" || czescPodpis == "" {
		return Dokument{}, fmt.Errorf("%w: zły format identyfikatora", ErrSzkicNiepoprawny)
	}

	oczekiwany, err := base64.RawURLEncoding.DecodeString(czescPodpis)
	if err != nil {
		return Dokument{}, fmt.Errorf("%w: podpisu nie da się odczytać", ErrSzkicNiepoprawny)
	}
	// Porównanie w czasie stałym — podpis jest sekretem.
	if !hmac.Equal(oczekiwany, p.hmac([]byte(czescPayload))) {
		return Dokument{}, fmt.Errorf("%w: podpis się nie zgadza. Szkic został zmieniony albo "+
			"pochodzi z serwera z innym kluczem. Przygotuj fakturę ponownie narzędziem przygotuj_fakture",
			ErrSzkicNiepoprawny)
	}

	payload, err := base64.RawURLEncoding.DecodeString(czescPayload)
	if err != nil {
		return Dokument{}, fmt.Errorf("%w: zawartości nie da się odczytać", ErrSzkicNiepoprawny)
	}
	var s Szkic
	if err := json.Unmarshal(payload, &s); err != nil {
		return Dokument{}, fmt.Errorf("%w: zawartość jest uszkodzona", ErrSzkicNiepoprawny)
	}
	if s.Wersja != wersjaSzkicu {
		return Dokument{}, fmt.Errorf("%w: szkic pochodzi z innej wersji serwera (v%d). "+
			"Przygotuj fakturę ponownie", ErrSzkicNiepoprawny, s.Wersja)
	}
	if p.teraz().Unix() > s.Wygasa {
		wiek := p.teraz().Sub(time.Unix(s.Wydany, 0)).Round(time.Minute)
		return Dokument{}, fmt.Errorf("%w: został przygotowany %s temu, a szkice są ważne %s. "+
			"Przygotuj fakturę ponownie narzędziem przygotuj_fakture i zatwierdź świeży szkic",
			ErrSzkicWygasl, wiek, TTLSzkicu)
	}
	return s.Dokument, nil
}

// UstawCzasDoTestow podmienia źródło czasu. Służy wyłącznie testom, które muszą
// wystawić szkic z przesuniętą datą, żeby sprawdzić zachowanie po TTL.
func (p *PodpisSzkicow) UstawCzasDoTestow(f func() time.Time) {
	p.terazFn = f
}

// hmac liczy HMAC-SHA256 z zakodowanego payloadu.
func (p *PodpisSzkicow) hmac(dane []byte) []byte {
	m := hmac.New(sha256.New, p.klucz)
	m.Write(dane)
	return m.Sum(nil)
}
