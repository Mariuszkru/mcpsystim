// Package systim zawiera klienta API fakturowego Systim (https://www.systim.pl/API/).
//
// API Systim nie jest REST-em: jeden endpoint, ciało form-urlencoded, nazwa metody
// w polu "act". Backend jest PHP-owy, przez co kształt JSON w odpowiedzi bywa
// niestabilny — te same pola potrafią przyjść jako liczba albo string, listy jako
// tablice albo mapy kluczowane ID, a brakujące liczby jako pusty string. Typy w tym
// pliku istnieją po to, żeby json.Unmarshal się na tym nie wywracał.
package systim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Response to koperta, w którą Systim pakuje każdą odpowiedź.
//
// Result trzymamy jako json.RawMessage, bo jego kształt zależy od metody: raz jest
// obiektem, raz mapą kluczowaną ID rekordu, a przy pustym wyniku bywa pustą tablicą.
type Response struct {
	Error  ResponseError   `json:"error"`
	Result json.RawMessage `json:"result"`
}

// ResponseError to pole "error" z odpowiedzi. Sukces sygnalizuje Code == 0.
type ResponseError struct {
	Code    FlexInt     `json:"code"`
	Message HTMLString  `json:"message"`
	Fields  FlexStrings `json:"fields"`
}

// FlexInt to liczba całkowita, która w JSON może przyjść jako number ("code": 0),
// jako string ("code": "0"), jako null albo jako pusty string.
type FlexInt int

// UnmarshalJSON przyjmuje number, string, null i "" — wszystkie warianty, jakie
// Systim potrafi zwrócić w polu liczbowym.
func (f *FlexInt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*f = 0
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("FlexInt: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		// Backend potrafi zwrócić "3.00" tam, gdzie oczekujemy liczby całkowitej.
		if d, err := decimal.NewFromString(s); err == nil {
			*f = FlexInt(d.IntPart())
			return nil
		}
		return fmt.Errorf("FlexInt: nie umiem odczytać %q jako liczby", s)
	}
	var d decimal.Decimal
	if err := json.Unmarshal(data, &d); err != nil {
		return fmt.Errorf("FlexInt: %w", err)
	}
	*f = FlexInt(d.IntPart())
	return nil
}

// Int zwraca wartość jako zwykły int.
func (f FlexInt) Int() int { return int(f) }

// NullableInt to liczba całkowita, która może być nieobecna. Ustawione mówi, czy
// wartość faktycznie przyszła — pusty string i null dają Ustawione == false.
type NullableInt struct {
	Wartosc   int
	Ustawione bool
}

// UnmarshalJSON znosi number, string, "" oraz null.
func (n *NullableInt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*n = NullableInt{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("NullableInt: %w", err)
		}
		if strings.TrimSpace(s) == "" {
			*n = NullableInt{}
			return nil
		}
	}
	var f FlexInt
	if err := f.UnmarshalJSON(data); err != nil {
		return fmt.Errorf("NullableInt: %w", err)
	}
	*n = NullableInt{Wartosc: int(f), Ustawione: true}
	return nil
}

// MarshalJSON zapisuje null dla wartości nieustawionej.
func (n NullableInt) MarshalJSON() ([]byte, error) {
	if !n.Ustawione {
		return []byte("null"), nil
	}
	return json.Marshal(n.Wartosc)
}

// NullableDecimal to kwota, która może być nieobecna. Kwoty trzymamy na
// decimal.Decimal, nigdy na float64 — to pieniądze.
type NullableDecimal struct {
	Wartosc   decimal.Decimal
	Ustawione bool
}

// UnmarshalJSON znosi number, string z kropką dziesiętną, "" oraz null.
func (n *NullableDecimal) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*n = NullableDecimal{}
		return nil
	}
	raw := string(data)
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("NullableDecimal: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = NullableDecimal{}
			return nil
		}
		// Systim używa kropki dziesiętnej, ale przecinek zdarza się w polach
		// wpisywanych ręcznie przez użytkownika panelu.
		raw = strings.ReplaceAll(s, ",", ".")
	}
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("NullableDecimal: nie umiem odczytać %q jako kwoty: %w", raw, err)
	}
	*n = NullableDecimal{Wartosc: d, Ustawione: true}
	return nil
}

// MarshalJSON zapisuje null dla wartości nieustawionej.
func (n NullableDecimal) MarshalJSON() ([]byte, error) {
	if !n.Ustawione {
		return []byte("null"), nil
	}
	return json.Marshal(n.Wartosc.String())
}

// String zwraca kwotę w formacie z kropką dziesiętną, a pusty string gdy nieustawiona.
func (n NullableDecimal) String() string {
	if !n.Ustawione {
		return ""
	}
	return n.Wartosc.String()
}

// HTMLString to string przepuszczony przez PHP-owe htmlspecialchars(). Odkodowujemy
// encje przy dekodowaniu, żeby "Kowalski &amp; Synowie" nie trafiło tak do modelu.
type HTMLString string

// UnmarshalJSON dekoduje string i odwraca htmlspecialchars(). Liczby przychodzące
// w miejsce stringa (PHP potrafi) też akceptujemy.
func (h *HTMLString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*h = ""
		return nil
	}
	if data[0] != '"' {
		// Nie-string (np. liczba) — bierzemy dosłownie, bez encji do odkodowania.
		*h = HTMLString(strings.Trim(string(data), `"`))
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("HTMLString: %w", err)
	}
	*h = HTMLString(html.UnescapeString(s))
	return nil
}

// String zwraca odkodowaną zawartość.
func (h HTMLString) String() string { return string(h) }

// FlexStrings to lista stringów, która w JSON bywa tablicą (["nip","nazwa"]),
// mapą asocjacyjną ({"nip":"..."}), pojedynczym stringiem albo jest nieobecna.
// Systim używa tego kształtu w error.fields.
type FlexStrings []string

// UnmarshalJSON normalizuje wszystkie te warianty do płaskiej listy.
func (f *FlexStrings) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*f = nil
		return nil
	}
	switch data[0] {
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("FlexStrings: %w", err)
		}
		out := make(FlexStrings, 0, len(raw))
		for _, r := range raw {
			out = append(out, skalarNaString(r))
		}
		*f = out
		return nil
	case '{':
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("FlexStrings: %w", err)
		}
		klucze := make([]string, 0, len(raw))
		for k := range raw {
			klucze = append(klucze, k)
		}
		sort.Strings(klucze)
		out := make(FlexStrings, 0, len(raw))
		for _, k := range klucze {
			// Przy mapie interesuje nas nazwa pola; wartość bywa komunikatem.
			if v := skalarNaString(raw[k]); v != "" && v != k {
				out = append(out, fmt.Sprintf("%s (%s)", k, v))
			} else {
				out = append(out, k)
			}
		}
		*f = out
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("FlexStrings: %w", err)
		}
		if s == "" {
			*f = nil
			return nil
		}
		*f = FlexStrings{html.UnescapeString(s)}
		return nil
	default:
		*f = FlexStrings{strings.TrimSpace(string(data))}
		return nil
	}
}

// skalarNaString sprowadza dowolny skalar JSON do stringa z odkodowanymi encjami.
func skalarNaString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return html.UnescapeString(s)
		}
	}
	return strings.TrimSpace(string(raw))
}

// idSetter pozwala dekoderowi list uzupełnić ID rekordu z klucza mapy.
type idSetter interface {
	ustawID(string)
}

// dekodujListe normalizuje "result" metod listujących do []T.
//
// Systim zwraca listy na trzy sposoby: jako mapę {"41": {...}, "42": {...}} gdzie
// kluczem jest ID rekordu, jako zwykłą tablicę obiektów, albo — przy pustym wyniku
// — jako pustą tablicę zamiast pustej mapy. Wszystkie trzy trafiają tu do []T
// z wypełnionym polem ID.
func dekodujListe[T any, PT interface {
	*T
	idSetter
}](raw json.RawMessage) ([]T, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}

	switch raw[0] {
	case '[':
		var elems []json.RawMessage
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, fmt.Errorf("dekodowanie listy jako tablicy: %w", err)
		}
		out := make([]T, 0, len(elems))
		for i, e := range elems {
			var v T
			if err := json.Unmarshal(e, PT(&v)); err != nil {
				return nil, fmt.Errorf("dekodowanie elementu %d: %w", i, err)
			}
			out = append(out, v)
		}
		return out, nil

	case '{':
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("dekodowanie listy jako mapy: %w", err)
		}
		klucze := make([]string, 0, len(m))
		for k := range m {
			klucze = append(klucze, k)
		}
		sort.Slice(klucze, func(i, j int) bool { return mniejszyKlucz(klucze[i], klucze[j]) })

		out := make([]T, 0, len(m))
		for _, k := range klucze {
			elem := bytes.TrimSpace(m[k])
			// Mapa kluczowana ID, ale wartość skalarna (np. {"1":"23%"}) — nie jest
			// to lista rekordów, więc nie udajemy, że jest.
			if len(elem) == 0 || (elem[0] != '{' && elem[0] != '[') {
				return nil, fmt.Errorf("dekodowanie listy: klucz %q wskazuje na skalar, nie na rekord", k)
			}
			var v T
			if err := json.Unmarshal(elem, PT(&v)); err != nil {
				return nil, fmt.Errorf("dekodowanie rekordu %q: %w", k, err)
			}
			PT(&v).ustawID(k)
			out = append(out, v)
		}
		return out, nil

	default:
		return nil, fmt.Errorf("dekodowanie listy: nieoczekiwany kształt result: %s", skrot(raw))
	}
}

// mniejszyKlucz porządkuje klucze mapy numerycznie, gdy się da — dzięki temu
// kolejność wyników jest stabilna i zgodna z intuicją ("2" przed "10").
func mniejszyKlucz(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

// skrot przycina surowy JSON do długości nadającej się do komunikatu błędu.
func skrot(raw []byte) string {
	const max = 120
	s := strings.TrimSpace(string(raw))
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
