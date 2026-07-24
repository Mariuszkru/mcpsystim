package systim

import (
	"errors"
	"fmt"
	"strings"
)

// Kody błędów zwracane przez API Systim w polu error.code.
const (
	KodOK               = 0  // sukces
	KodDostepZabroniony = 2  // blokada po IP albo throttling za zbyt intensywne odpytywanie
	KodBledneDaneLog    = 4  // nieprawidłowy login lub hasło
	KodBlednePola       = 6  // nie wypełniono poprawnie wymaganych pól (także: wskazane ID nie istnieje)
	KodBrakDanych       = 8  // brak danych / pusty wynik
	KodBrakFunkcji      = 10 // ta wersja programu nie pozwala na tę operację
	KodBrakSesji        = 13 // brak sesji użytkownika (wygasły token)
	KodMiesiacZamkniety = 16 // miesiąc jest zamknięty
)

// SystimError to błąd zwrócony przez API. Fields zawiera nazwy pól, które backend
// uznał za niepoprawnie wypełnione.
type SystimError struct {
	Code    int
	Message string
	Fields  []string
	// Act to nazwa metody API, przy której błąd wystąpił. Puste w sentinelach.
	Act string
}

// Sentinele do porównywania przez errors.Is. Porównanie idzie po samym kodzie,
// więc errors.Is(err, ErrBrakSesji) działa niezależnie od treści komunikatu.
var (
	ErrDostepZabroniony = &SystimError{Code: KodDostepZabroniony}
	ErrBledneDaneLog    = &SystimError{Code: KodBledneDaneLog}
	ErrBlednePola       = &SystimError{Code: KodBlednePola}
	ErrBrakDanych       = &SystimError{Code: KodBrakDanych}
	ErrBrakFunkcji      = &SystimError{Code: KodBrakFunkcji}
	ErrBrakSesji        = &SystimError{Code: KodBrakSesji}
	ErrMiesiacZamkniety = &SystimError{Code: KodMiesiacZamkniety}
)

// Error implementuje interfejs error.
func (e *SystimError) Error() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Systim: błąd %d", e.Code))
	if e.Act != "" {
		b.WriteString(fmt.Sprintf(" (metoda %s)", e.Act))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if len(e.Fields) > 0 {
		b.WriteString(fmt.Sprintf(" [pola: %s]", strings.Join(e.Fields, ", ")))
	}
	return b.String()
}

// Is pozwala porównywać błędy sentinelami po kodzie, ignorując komunikat i pola.
func (e *SystimError) Is(target error) bool {
	var t *SystimError
	if !errors.As(target, &t) {
		return false
	}
	return e.Code == t.Code
}

// KomunikatPL zwraca komunikat po polsku przeznaczony dla modelu i użytkownika:
// treść od Systim plus podpowiedź, co z danym kodem zrobić.
func (e *SystimError) KomunikatPL() string {
	var b strings.Builder
	if e.Message != "" {
		b.WriteString(e.Message)
	} else {
		b.WriteString(fmt.Sprintf("API Systim zwróciło błąd o kodzie %d", e.Code))
	}
	if len(e.Fields) > 0 {
		b.WriteString(fmt.Sprintf("\nPola wypełnione niepoprawnie: %s.", strings.Join(e.Fields, ", ")))
	}
	if p := e.podpowiedz(); p != "" {
		b.WriteString("\n" + p)
	}
	return b.String()
}

// podpowiedz mapuje kod błędu na wskazówkę, co użytkownik może z tym zrobić.
func (e *SystimError) podpowiedz() string {
	switch e.Code {
	case KodDostepZabroniony:
		return "Dostęp zabroniony (kod 2). Zwykle oznacza throttling za zbyt intensywne " +
			"odpytywanie API albo blokadę po adresie IP. Odczekaj kilkadziesiąt sekund i " +
			"ogranicz liczbę wywołań; jeśli błąd się utrzymuje, sprawdź w panelu Systim, " +
			"czy adres IP serwera nie jest zablokowany."
	case KodBledneDaneLog:
		return "Nieprawidłowy login lub hasło (kod 4). Sprawdź SYSTIM_LOGIN i SYSTIM_PASS — " +
			"hasło do API generuje się osobno w panelu, nie jest to hasło do logowania w przeglądarce."
	case KodBlednePola:
		return "Nie wypełniono poprawnie wymaganych pól (kod 6). Ten kod pojawia się także " +
			"wtedy, gdy wskazane ID nie istnieje — sprawdź id_kontrahenta, id_szablonu i id_numeracji."
	case KodBrakDanych:
		return "Brak danych — zapytanie nie zwróciło żadnego rekordu (kod 8)."
	case KodBrakFunkcji:
		return "Ta wersja programu Systim nie pozwala na tę operację (kod 10). Funkcja wymaga " +
			"wykupienia w panelu."
	case KodBrakSesji:
		return "Brak sesji użytkownika (kod 13) — token wygasł albo ktoś zalogował się do panelu " +
			"WWW, co kasuje wszystkie sesje API. Klient próbuje przelogować się automatycznie; " +
			"jeśli widzisz ten komunikat, ponowienie też się nie powiodło."
	case KodMiesiacZamkniety:
		return "Miesiąc jest zamknięty (kod 16) — okres księgowy, w którym miał powstać dokument, " +
			"został zamknięty w Systim. Otwórz okres w panelu albo wystaw dokument z datą " +
			"z bieżącego, otwartego miesiąca."
	default:
		return ""
	}
}

// KomunikatPL zwraca polski komunikat dla dowolnego błędu — dla błędów Systim
// z podpowiedzią, dla pozostałych samą treść.
func KomunikatPL(err error) string {
	if err == nil {
		return ""
	}
	var se *SystimError
	if errors.As(err, &se) {
		return se.KomunikatPL()
	}
	return err.Error()
}
