package systim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// Rekord to pojedynczy wiersz kartoteki zwrócony przez metodę listującą.
//
// Dokumentacja Systim nie podaje pełnej listy pól ani ich stabilnych nazw, a backend
// bywa zmienny (raz "nazwa", raz "nazwa_firmy"). Zamiast zgadywać sztywną strukturę
// i cicho gubić dane, spłaszczamy każdy rekord do mapy nazwa→wartość i sięgamy po
// pola z listą kandydatów. Dzięki temu nowe albo inaczej nazwane pole nadal dociera
// do użytkownika zamiast wyparować przy dekodowaniu.
type Rekord struct {
	// ID rekordu. Przy result w formie mapy pochodzi z klucza mapy.
	ID string
	// Pola to wszystkie skalarne pola rekordu, z odkodowanymi encjami HTML.
	// Wartości zagnieżdżone (obiekty, tablice) trafiają tu jako kompaktowy JSON.
	Pola map[string]string
}

// ustawID uzupełnia ID z klucza mapy, o ile sam rekord go nie zawierał.
func (r *Rekord) ustawID(id string) {
	if r.ID == "" {
		r.ID = id
	}
	if r.Pola == nil {
		r.Pola = map[string]string{}
	}
	if _, ok := r.Pola["id"]; !ok {
		r.Pola["id"] = id
	}
}

// UnmarshalJSON spłaszcza obiekt JSON do mapy stringów, odkodowując po drodze
// encje HTML wstawione przez PHP-owe htmlspecialchars().
func (r *Rekord) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*r = Rekord{Pola: map[string]string{}}
		return nil
	}
	var surowe map[string]json.RawMessage
	if err := json.Unmarshal(data, &surowe); err != nil {
		return fmt.Errorf("Rekord: %w", err)
	}
	pola := make(map[string]string, len(surowe))
	for k, v := range surowe {
		v = bytes.TrimSpace(v)
		if len(v) == 0 || bytes.Equal(v, []byte("null")) {
			pola[k] = ""
			continue
		}
		switch v[0] {
		case '{', '[':
			// Zagnieżdżoną strukturę zachowujemy jako JSON — rzadko potrzebna,
			// ale lepsza niż jej utrata.
			pola[k] = string(v)
		case '"':
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				pola[k] = strings.Trim(string(v), `"`)
				continue
			}
			pola[k] = html.UnescapeString(s)
		default:
			pola[k] = string(v)
		}
	}
	*r = Rekord{Pola: pola}
	if id := pola["id"]; id != "" {
		r.ID = id
	}
	return nil
}

// Pole zwraca pierwszą niepustą wartość spośród podanych nazw kandydatów.
func (r Rekord) Pole(klucze ...string) string {
	for _, k := range klucze {
		if v, ok := r.Pola[k]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Klucze zwraca posortowane nazwy pól rekordu.
func (r Rekord) Klucze() []string {
	out := make([]string, 0, len(r.Pola))
	for k := range r.Pola {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Nazwa zgaduje nazwę rekordu spośród typowych wariantów używanych przez Systim.
func (r Rekord) Nazwa() string {
	return r.Pole("nazwa", "nazwa_firmy", "nazwa_pelna", "firma", "nazwa_produktu", "tytul", "name")
}

// NIP zwraca NIP kontrahenta.
func (r Rekord) NIP() string {
	return r.Pole("nip", "nip_firmy", "vat_id", "numer_nip")
}

// TekstDoSzukania skleja wartości rekordu w jeden łańcuch do filtrowania po stronie
// serwera — API Systim nie ma parametru wyszukiwania w metodach listujących.
func (r Rekord) TekstDoSzukania() string {
	var b strings.Builder
	for _, k := range r.Klucze() {
		b.WriteString(r.Pola[k])
		b.WriteByte('\n')
	}
	return b.String()
}
