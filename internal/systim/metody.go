package systim

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ListVatRates zwraca stawki VAT wraz z ich ID w Systim.
//
// To ID, a nie procent, trafia do pola stawka_vat przy wystawianiu faktury.
func (c *Client) ListVatRates(ctx context.Context) ([]Rekord, error) {
	raw, err := c.Wywolaj(ctx, "listVatRates", url.Values{})
	if err != nil {
		return nil, pustaListaNieJestBledem(err)
	}
	return dekodujListe[Rekord](raw)
}

// ListCompanies zwraca kartotekę kontrahentów.
//
// API nie przyjmuje parametru wyszukiwania, więc filtrowanie odbywa się po stronie
// tego serwera.
func (c *Client) ListCompanies(ctx context.Context) ([]Rekord, error) {
	raw, err := c.Wywolaj(ctx, "listCompanies", url.Values{})
	if err != nil {
		return nil, pustaListaNieJestBledem(err)
	}
	return dekodujListe[Rekord](raw)
}

// ListProducts zwraca kartotekę produktów i usług.
func (c *Client) ListProducts(ctx context.Context) ([]Rekord, error) {
	raw, err := c.Wywolaj(ctx, "listProducts", url.Values{})
	if err != nil {
		return nil, pustaListaNieJestBledem(err)
	}
	return dekodujListe[Rekord](raw)
}

// FiltrFaktur zawęża listę faktur sprzedaży. Wszystkie pola są opcjonalne.
type FiltrFaktur struct {
	// IDs to lista ID faktur; do API trafia jako CSV.
	IDs []string
	// DataOd i DataDo w formacie RRRR-MM-DD, po dacie wystawienia.
	DataOd string
	DataDo string
}

// ListSellInvoices zwraca faktury sprzedaży zawężone filtrem.
func (c *Client) ListSellInvoices(ctx context.Context, f FiltrFaktur) ([]Rekord, error) {
	params := url.Values{}
	if len(f.IDs) > 0 {
		params.Set("ids", strings.Join(f.IDs, ","))
	}
	if f.DataOd != "" {
		params.Set("data_wystawienia_od", f.DataOd)
	}
	if f.DataDo != "" {
		params.Set("data_wystawienia_do", f.DataDo)
	}
	raw, err := c.Wywolaj(ctx, "listSellInvoices", params)
	if err != nil {
		return nil, pustaListaNieJestBledem(err)
	}
	return dekodujListe[Rekord](raw)
}

// PlikPDF to faktura pobrana z Systim w formie pliku.
type PlikPDF struct {
	// Base64 to zawartość pliku zakodowana base64, dokładnie jak zwraca API.
	Base64 string
	// Nazwa to proponowana nazwa pliku.
	Nazwa string
}

// odpowiedzPDF to kształt result metody getSellInvoicePDF.
type odpowiedzPDF struct {
	File HTMLString `json:"file"`
	Name HTMLString `json:"name"`
}

// GetSellInvoicePDF pobiera PDF faktury sprzedaży o podanym ID.
func (c *Client) GetSellInvoicePDF(ctx context.Context, id string) (PlikPDF, error) {
	if strings.TrimSpace(id) == "" {
		return PlikPDF{}, errors.New("pobranie PDF: ID faktury jest wymagane")
	}
	params := url.Values{}
	params.Set("id", id)

	raw, err := c.Wywolaj(ctx, "getSellInvoicePDF", params)
	if err != nil {
		return PlikPDF{}, err
	}
	var o odpowiedzPDF
	if err := json.Unmarshal(raw, &o); err != nil {
		return PlikPDF{}, fmt.Errorf("pobranie PDF: nie umiem odczytać odpowiedzi: %w", err)
	}
	if o.File == "" {
		return PlikPDF{}, fmt.Errorf("pobranie PDF: API nie zwróciło zawartości pliku dla faktury %s", id)
	}
	return PlikPDF{Base64: o.File.String(), Nazwa: o.Name.String()}, nil
}

// pustaListaNieJestBledem zamienia kod 8 ("brak danych") na pustą listę.
//
// Systim sygnalizuje pusty wynik błędem, ale dla metody listującej brak rekordów to
// poprawna odpowiedź, nie awaria.
func pustaListaNieJestBledem(err error) error {
	if errors.Is(err, ErrBrakDanych) {
		return nil
	}
	return err
}
