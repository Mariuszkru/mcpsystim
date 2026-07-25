package tools

import (
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Mariuszkru/mcpsystim/internal/systim"
)

// formatDaty to format, w jakim API Systim przyjmuje i zwraca daty.
const formatDaty = "2006-01-02"

// tekst pakuje czytelną odpowiedź dla modelu.
//
// SDK samo wypełniłoby Content JSON-em struktury wyjściowej, ale czytelny tekst
// po polsku jest dla modelu wyraźniejszy.
//
// Uwaga: tekst musi być samowystarczalny. StructuredContent jedzie równolegle,
// ale nie każdy klient MCP go pokazuje — klient Chat w Claude podaje modelowi
// sam Content. Każdy identyfikator potrzebny do kolejnego wywołania (szkic_id,
// id_faktury, id_kontrahenta, id_produktu) musi więc znaleźć się w tekście.
func tekst(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: s}},
	}
}

// bladDlaModelu opakowuje błąd w komunikat po polsku, dokładając podpowiedź
// przypisaną do kodu błędu Systim.
func bladDlaModelu(operacja string, err error) error {
	var se *systim.SystimError
	if errors.As(err, &se) {
		return fmt.Errorf("%s nie powiodło się.\n%s", operacja, se.KomunikatPL())
	}
	return fmt.Errorf("%s nie powiodło się: %w", operacja, err)
}

// sprawdzDate waliduje datę w formacie RRRR-MM-DD i zwraca ją w postaci
// znormalizowanej.
func sprawdzDate(wartosc, nazwaPola string) (string, error) {
	if wartosc == "" {
		return "", fmt.Errorf("pole %s jest wymagane (format RRRR-MM-DD, np. 2026-07-24)", nazwaPola)
	}
	t, err := time.Parse(formatDaty, wartosc)
	if err != nil {
		return "", fmt.Errorf("pole %s ma wartość %q, a oczekuję formatu RRRR-MM-DD, np. 2026-07-24", nazwaPola, wartosc)
	}
	return t.Format(formatDaty), nil
}
