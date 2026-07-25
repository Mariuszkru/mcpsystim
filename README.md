# systim-mcp

Serwer MCP (Model Context Protocol) integrujący Claude z systemem fakturowym
[Systim](https://www.systim.pl/API/). Napisany w Go, uruchamiany jako kontener Docker.

Repozytorium: <https://github.com/Mariuszkru/mcpsystim>

Zakres jest celowo wąski: **wystawianie faktur sprzedaży** plus minimum narzędzi
pomocniczych potrzebnych, żeby fakturę dało się poprawnie złożyć.

---

## ⚠️ Zanim zaczniesz — o bezpieczeństwie

**API Systim nie ma granularnych uprawnień.** Jeden token daje pełny dostęp do
konta, łącznie z usuwaniem danych. Serwer, który tu stawiasz, jest bramką do
księgowości firmy.

Wynikają z tego trzy zasady, których ten projekt pilnuje:

1. **Endpoint `/mcp` bez uwierzytelnienia jest nie do przyjęcia.** Serwer wymaga
   ważnego tokenu OAuth 2.1. Wyłączenie tej walidacji (`SYSTIM_AUTH_DISABLED=true`)
   jest możliwe wyłącznie do testów lokalnych i powoduje głośne ostrzeżenie
   w logach przy każdym starcie.
2. **Wystawienie dokumentu jest dwuetapowe.** `przygotuj_fakture` tylko liczy
   i pokazuje kwoty, `zatwierdz_fakture` dopiero zapisuje. Użytkownik musi zobaczyć
   kwoty, zanim powstanie dokument księgowy.
3. **Narzędzia usuwającego nie ma.** Metoda `delSellInvoice` istnieje w API, ale
   celowo nie jest wystawiona jako narzędzie MCP.

Załóż w Systim **osobnego użytkownika do integracji** i wygeneruj mu hasło API.
Nie używaj konta, którym logujesz się do panelu.

---

## Wymagania

| Element | Wersja / uwagi |
|---|---|
| Go | 1.25+ — patrz [Decyzje projektowe](#decyzje-projektowe-i-odstępstwa) |
| Docker | z obsługą BuildKit |
| Konto Systim | z dostępem do API i wygenerowanym hasłem API |
| Dostawca tożsamości | authentik (samohostowany) |
| Publiczny adres HTTPS | Claude łączy się z chmury Anthropic — localhost nie zadziała |

---

## Pierwsze uruchomienie

Kolejność ma znaczenie. Trzy z tych kroków wymagają zajrzenia do panelu Systim,
bo API nie udostępnia metod, które by je zautomatyzowały.

### 1. Skopiuj konfigurację i uzupełnij dane dostępowe

```bash
cp .env.example .env
```

Uzupełnij `SYSTIM_KONTO`, `SYSTIM_LOGIN`, `SYSTIM_PASS` oraz wygeneruj klucz
podpisujący szkice:

```bash
openssl rand -base64 48
```

### 2. Znajdź `id_szablonu` i `id_numeracji` — ręcznie, w panelu

**Nie ma metody API, która by je wylistowała.** Brak któregokolwiek z tych dwóch
pól powoduje odrzucenie dokumentu przez API.

- **ID szablonu**: Panel Systim → Ustawienia → Szablony wydruku, kolumna `ID`.
  **Każdy typ dokumentu ma własny szablon**, a konto może mieć dodatkowo warianty
  obcojęzyczne (np. Pro Forma EN, Pro Forma DE).
- **ID numeracji**: Panel Systim → Ustawienia → Numeracja dokumentów. Kolumna `ID`
  wskazuje serię, a **każdy typ dokumentu ma własną**.

Wpisz je do `SYSTIM_ID_SZABLONU` i `SYSTIM_ID_NUMERACJI`.

> **Numeracja jest przypisana do typu dokumentu.** Wysłanie numeracji faktury VAT
> przy rodzaju „pro forma" kończy się odrzuceniem dokumentu komunikatem
> „błędne przypisanie rodzaju dokumentu do numeracji". Dlatego `SYSTIM_ID_NUMERACJI`
> przyjmuje mapę `rodzaj → ID`:
>
> ```
> SYSTIM_ID_NUMERACJI={"0":1,"1":5}
> ```
>
> **To samo dotyczy szablonu wydruku** — `SYSTIM_ID_SZABLONU` też przyjmuje mapę.
>
> Standardowe ID Systim, używane jako domyślne, gdy nie nadpiszesz:
>
> | `rodzaj` | Dokument | Numeracja | Szablon |
> |---|---|---|---|
> | `0` | faktura VAT | 1 | 43 |
> | `1` | pro forma | 5 | 1 |
> | `6` | paragon fiskalny | 39 | 15 |
> | `15` | paragon | 9 | 15 |
> | `22` | rachunek | 16 | 22 |
> | `26` | oferta | 21 | 26 |
>
> Pojedyncza liczba nadal działa w obu zmiennych, ale obowiązuje dla wszystkich
> rodzajów naraz — czyli tylko wtedy, gdy wystawiasz jeden typ dokumentu.

### 3. Odczytaj ID stawek VAT narzędziem `lista_stawek_vat`

Pole `stawka_vat` w API przyjmuje **ID stawki w Systim, a nie procent**. To jedna
z najczęstszych pomyłek przy tej integracji.

Uruchom serwer z tymczasową wartością `SYSTIM_VAT_IDS` (może być `{"23":1}`) i wywołaj
narzędzie `lista_stawek_vat`. Najprościej lokalnie, po stdio:

```bash
docker run -i --rm --env-file .env -e SYSTIM_TRANSPORT=stdio systim-mcp:dev
```

Narzędzie zwróci listę w postaci `ID 1 — 23%`, `ID 5 — zw` i tak dalej.

### 4. Uzupełnij `SYSTIM_VAT_IDS` i zrestartuj

```
SYSTIM_VAT_IDS={"23":1,"8":2,"5":3,"0":4,"zw":5}
```

Kluczem jest procent bez znaku `%` albo oznaczenie stawki nieprocentowej
(`zw`, `np`, `oo`). Dzięki temu mapowaniu użytkownik narzędzia pisze po prostu
„23" albo „zw", a serwer sam podstawia ID.

### 5. Wystaw pierwszy dokument jako **pro formę**

Zanim wystawisz prawdziwą fakturę VAT, sprawdź całą ścieżkę na dokumencie, który
nie jest fakturą. W `przygotuj_fakture` podaj `rodzaj: 1` (pro forma).

### 6. Sprawdź dokument w panelu Systim

Zweryfikuj kwoty, dane nabywcy, szablon i numer. Kwoty liczy ten serwer — API
Systim ich nie wylicza — więc to jest moment na potwierdzenie, że wszystko się zgadza.

### 7. Dodaj jeszcze jeden dokument ręcznie i sprawdź numerację

**Dokumentacja Systim wprost przed tym ostrzega.** Wystaw jeden dokument ręcznie
w panelu, potem jeszcze jeden przez API, i sprawdź, czy w numeracji nie zrobiła się
dziura. Jeśli tak — problem jest w konfiguracji numeracji, a nie w tym serwerze,
i trzeba go rozwiązać, zanim zaczniesz wystawiać dokumenty produkcyjnie.

Do weryfikacji służy narzędzie `lista_faktur`.

---

## Narzędzia MCP

| Narzędzie | Adnotacja | Opis |
|---|---|---|
| `lista_stawek_vat` | read-only | Stawki VAT wraz z ID — do jednorazowej konfiguracji |
| `szukaj_kontrahenta` | read-only | Po nazwie lub NIP; odporne na myślniki i wielkość liter, maks. 25 wyników |
| `szukaj_produktu` | read-only | Po nazwie, kodzie lub opisie, maks. 25 wyników |
| `przygotuj_fakture` | read-only | Liczy kwoty, zwraca podgląd i `szkic_id`. **Niczego nie zapisuje** |
| `zatwierdz_fakture` | **destrukcyjne, nieidempotentne** | Wystawia dokument. **Nieodwracalne** |
| `lista_faktur` | read-only | Faktury w zakresie dat — do weryfikacji |
| `pobierz_pdf` | zapis na dysk | Zapisuje PDF do wolumenu i zwraca ścieżkę |

### Dlaczego `przygotuj_fakture` i `zatwierdz_fakture` są rozdzielone

Faktura to dokument księgowy. Użytkownik musi zobaczyć kwoty, zanim je zatwierdzi,
a model nie może wystawić dokumentu „przy okazji" realizowania innego polecenia.

`przygotuj_fakture` zwraca `szkic_id` — **samowystarczalny token podpisany
HMAC-SHA256** kluczem z `SYSTIM_SZKIC_KLUCZ`, zawierający w środku wszystkie
pozycje, kwoty i czas wygaśnięcia (30 minut). Nie jest to klucz do mapy w pamięci
procesu, więc:

- szkic przeżywa restart serwera,
- działa przy wielu replikach za load balancerem,
- zmiana choćby jednej kwoty unieważnia podpis i szkic zostaje odrzucony.

To ostatnie jest istotne: użytkownik akceptuje **konkretne kwoty**, więc kwoty nie
mogą się zmienić między podglądem a zapisem.

### `pobierz_pdf` nie zwraca base64

Zawartość pliku trafia na zamontowany wolumen, a narzędzie zwraca samą ścieżkę.
PDF w odpowiedzi zapchałby kontekst rozmowy.

---

## Konfiguracja

Wyłącznie przez zmienne środowiskowe, walidowane raz przy starcie. Brak wymaganej
zmiennej **zatrzymuje proces z pełną listą braków**, zamiast wysypać się dopiero
przy pierwszym wywołaniu narzędzia.

Komplet zmiennych z komentarzami, gdzie w panelu Systim znaleźć każdą wartość,
znajduje się w [`.env.example`](.env.example).

| Zmienna | Domyślnie | Opis |
|---|---|---|
| `SYSTIM_KONTO` | — | Poddomena konta (`abcd` dla `abcd.systim.pl`) |
| `SYSTIM_LOGIN` | — | Użytkownik z wygenerowanym hasłem API |
| `SYSTIM_PASS` | — | Hasło do API (inne niż hasło do panelu) |
| `SYSTIM_ID_SZABLONU` | — | Mapa `rodzaj → ID szablonu`, np. `{"0":43,"1":1}` |
| `SYSTIM_ID_NUMERACJI` | — | Mapa `rodzaj → ID numeracji`, np. `{"0":1,"1":5}` |
| `SYSTIM_VAT_IDS` | — | JSON: mapa stawka → ID |
| `SYSTIM_TRANSPORT` | `http` | `http` albo `stdio` |
| `SYSTIM_ADDR` | `:8000` | Adres nasłuchu |
| `SYSTIM_KATALOG_PDF` | `/data/faktury` | Katalog na pobrane PDF-y |
| `SYSTIM_TIMEOUT` | `30s` | Timeout wywołania API Systim |
| `SYSTIM_CACHE_KARTOTEK` | `5m` | Czas życia cache kartotek kontrahentów i produktów; `0` wyłącza |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `SYSTIM_LOG_PLIK` | — | Plik, do którego dublowane są logi; puste = tylko stdout |
| `SYSTIM_LOG_MAX_MB` | `10` | Rozmiar, po którym plik logu jest rotowany |
| `SYSTIM_LOG_KOPIE` | `3` | Liczba trzymanych kopii (`.1`, `.2`, `.3`); `0` = brak kopii |
| `SYSTIM_DOMYSLNA_FORMA_PLATNOSCI` | — | Forma płatności użyta, gdy narzędzie jej nie poda |
| `SYSTIM_FORMY_PLATNOSCI` | standardowe ID | Mapa `nazwa → ID` formy płatności |
| `SYSTIM_PUBLIC_URL` | — | Publiczny adres HTTPS serwera |
| `SYSTIM_SZKIC_KLUCZ` | — | Klucz HMAC, min. 32 bajty |
| `OIDC_ISSUER` | — | Issuer authentika, z ukośnikiem: `https://auth.firma.pl/application/o/<slug>/` |
| `OIDC_AUDIENCE` | — | Oczekiwane `aud` — w authentiku domyślnie `client_id` |
| `OIDC_SCOPE` | — | Scope wymagany w tokenie |
| `OIDC_SCOPES_REQUESTED` | `openid offline_access` | Scope'y ogłaszane klientowi; `offline_access` jest konieczny dla refresh tokenu |
| `SYSTIM_AUTH_DISABLED` | `false` | Wyłącza walidację tokenu — **tylko testy lokalne** |
| `SYSTIM_DODATKOWE_ORIGINY` | — | Dodatkowe dozwolone `Origin`, po przecinku |
| `SYSTIM_MAX_POZYCJI` | `200` | Limit pozycji na dokumencie |
| `SYSTIM_MAX_CIALO` | `4194304` | Limit rozmiaru żądania do `/mcp` |
| `SYSTIM_WYLACZ_OCHRONE_LOCALHOST` | `false` | Patrz [Rozwiązywanie problemów](#rozwiązywanie-problemów) |

---

## Uwierzytelnianie

Uwierzytelnianie opiera się na **samohostowanym authentiku**. Adresy w tym
rozdziale są przykładowe — podstaw własną instancję.

### Podział ról

Zgodna implementacja OAuth 2.1 to dużo pracy i łatwo o subtelny błąd
bezpieczeństwa, więc **ten projekt nie zawiera własnego serwera autoryzacji**.
Serwer MCP pełni wyłącznie rolę **resource servera**: hostuje Protected Resource
Metadata i waliduje tokeny wydane przez authentika.

Authentik nadaje się tu dobrze: jest samohostowany, więc dane logowania do
księgowości nie wychodzą poza infrastrukturę firmy; ogłasza
`code_challenge_methods_supported: ["plain", "S256"]`, czego wymaga Claude;
wspiera `authorization_code` i `refresh_token`; a całą konfigurację providera da
się opisać deklaratywnie blueprintem.

### Jak to działa

1. Claude odpytuje `/mcp` bez tokenu i dostaje **`401`** z nagłówkiem:
   ```
   WWW-Authenticate: Bearer realm="systim-mcp",
     resource_metadata="https://mcp.firma.pl/.well-known/oauth-protected-resource",
     scope="systim:faktury openid offline_access", error="invalid_token"
   ```
2. Pobiera **Protected Resource Metadata** spod wskazanego adresu i dowiaduje się,
   który serwer autoryzacji obsługuje ten zasób.
3. Przeprowadza flow **`authorization_code` + PKCE (S256)**, a potem odświeża token
   przez **`refresh_token`**.
4. Serwer waliduje JWT: podpis przez JWKS (z cache i obsługą rotacji kluczy),
   `iss`, `aud`, `exp`, `nbf` oraz wymagany scope.

Czysty `client_credentials` (maszyna–maszyna, bez udziału użytkownika) **nie jest
wspierany przez Claude** i nie ma tu zastosowania.

Walidacja jest zrealizowana jako `http.Handler` middleware opakowujący handler MCP,
z możliwością wyłączenia przez `SYSTIM_AUTH_DISABLED=true`.

### Konfiguracja authentika

Gotowy blueprint: [`deploy/authentik/blueprint-systim-mcp.yaml`](deploy/authentik/blueprint-systim-mcp.yaml).
Tworzy scope `systim:faktury`, provider OAuth2 i aplikację.

Zastosowanie na własnej instancji — jedna z dwóch dróg:

- **authentik → Customization → Blueprints → Create**, wskazując plik, albo
- montując katalog `deploy/authentik` do `/blueprints/custom` w kontenerach
  `authentik-server` **i** `authentik-worker` (blueprinty stosuje worker).

**Przed zastosowaniem zmień `client_secret`** w sekcji `context` blueprintu:

```bash
openssl rand -base64 48
```

Po zaaplikowaniu ustaw w `.env`:

```
OIDC_ISSUER=https://auth.firma.pl/application/o/systim-mcp/
OIDC_AUDIENCE=systim-mcp-connector
OIDC_SCOPE=systim:faktury
```

### Trzy szczegóły authentika, które łatwo przeoczyć

**1. Issuer zawiera slug aplikacji i kończy się ukośnikiem.**

W domyślnym trybie „per provider" issuer ma postać
`https://auth.firma.pl/application/o/<slug-aplikacji>/`, a nie samo
`https://auth.firma.pl/`. Sprawdź go u źródła:

```bash
curl -s https://auth.firma.pl/application/o/systim-mcp/.well-known/openid-configuration | jq .issuer
```

**2. Bez `offline_access` nie ma refresh tokenu.**

Od wersji 2024.2 authentik wydaje refresh token **tylko wtedy**, gdy klient wprost
poprosił o scope `offline_access`. Bez niego konektor zacznie działać normalnie,
a rozłączy się dopiero po wygaśnięciu tokenu dostępowego — objaw mylący, bo
wygląda na przypadkową awarię sieci.

Dlatego serwer ogłasza w `WWW-Authenticate` **szerszą listę scope'ów niż sprawdza**:
`systim:faktury openid offline_access`. Sam token musi mieć jedynie
`systim:faktury`. Listę nadpisuje `OIDC_SCOPES_REQUESTED`, a `offline_access` musi
też być wśród `property_mappings` providera (blueprint już go tam ma).

**3. `aud` to domyślnie `client_id`.**

Authentik wstawia do `aud` identyfikator klienta, więc `OIDC_AUDIENCE` należy
ustawić na `systim-mcp-connector` — bez żadnego dodatkowego mapowania. Jeśli
wolisz, żeby `aud` było adresem zasobu, zmień wyrażenie scope mappingu
w blueprincie na `return {"aud": "https://mcp.firma.pl/mcp"}` i wpisz tę samą
wartość w `OIDC_AUDIENCE`.

### Sprawdzenie konfiguracji przed podpięciem konektora

Zamiast diagnozować nieudane podpięcie w claude.ai, uruchom test integracyjny.
Sprawdza discovery, PKCE S256 i to, czy parser przyjmuje wszystkie klucze z JWKS:

```bash
AUTH_ISSUER=https://auth.firma.pl/application/o/systim-mcp/ go test ./internal/auth/ -run TestIntegracjaZPrawdziwymIdP -v
```

### Rejestracja klienta

Blueprint zakłada **klienta poufnego ze stałym `client_id` i `client_secret`**,
wpisywanym ręcznie w „Advanced settings" konektora. Dla jednej organizacji
i jednego klienta jest to prostsze i w zupełności wystarczające — Dynamic Client
Registration nie jest potrzebna.

- `client_id`: `systim-mcp-connector`
- `client_secret`: wartość, którą wygenerowałeś w blueprincie

> Jeśli zmienisz `client_type` na `public` (klient bez sekretu), zadbaj o **rotację
> refresh tokenów** — nowy token musi wrócić w tej samej odpowiedzi, w której
> unieważniany jest stary. W authentiku odpowiada za to ustawienie providera
> „Refresh token rotation".

### Endpoint `/token` musi przyjmować `application/x-www-form-urlencoded`

Serwer autoryzacji skonfigurowany wyłącznie na JSON zwróci `415` i cały flow się
wywali. Authentik robi to poprawnie domyślnie — warto o tym pamiętać, jeśli przed
nim stoi proxy przepisujące żądania.

### Ograniczenie dostępu do aplikacji

Blueprint tworzy aplikację widoczną dla wszystkich uwierzytelnionych użytkowników.
**Zawęź to**: authentik → Applications → `systim-mcp` → Policy bindings, i przypisz
wąską grupę. Dostęp do tego konektora to pełny dostęp do księgowości firmy.

### Lokalny test całego flow

```bash
docker compose --profile dev up
```

Podnosi kompletnego authentika (server, worker, PostgreSQL, Redis) na
`http://127.0.0.1:9000` z automatycznie zastosowanym blueprintem. W `.env` ustaw:

```
OIDC_ISSUER=http://127.0.0.1:9000/application/o/systim-mcp/
OIDC_AUDIENCE=systim-mcp-connector
OIDC_SCOPE=systim:faktury
```

Panel administracyjny: `http://127.0.0.1:9000` (`akadmin` / `akadmin`).

Ten profil jest zbędny, jeśli masz już własnego authentika — służy do przejścia
flow bez ruszania instancji produkcyjnej.

### Alternatywa: `static_headers`

Anthropic udostępnia w wersji **beta** uwierzytelnianie stałym nagłówkiem
(`static_headers`), które wymaga kontaktu w sprawie wcześniejszego dostępu.
Jest to opcja warta rozważenia, jeśli stawianie IdP jest w danym środowisku
nieproporcjonalne do skali. Ten projekt **nie buduje na niej głównej ścieżki** —
domyślną i przetestowaną drogą jest OAuth 2.1 z authentikiem.
## Uruchomienie

### Docker Compose (docelowo)

```bash
git clone https://github.com/Mariuszkru/mcpsystim.git
cd mcpsystim
cp .env.example .env    # uzupełnij wartości
docker compose up -d --build
```

Serwer nasłuchuje na `127.0.0.1:8000`. Ruch z internetu ma przechodzić przez
**reverse proxy z certyfikatem TLS** — kontener celowo nie jest wystawiony wprost.

Endpointy:

| Ścieżka | Przeznaczenie |
|---|---|
| `/mcp` | Streamable HTTP — endpoint MCP (wymaga tokenu) |
| `/healthz` | Sonda dla `HEALTHCHECK` i platformy hostingowej (bez tokenu) |
| `/.well-known/oauth-protected-resource` | Protected Resource Metadata (bez tokenu) |

Starego transportu **SSE nie ma i nie będzie** — infrastruktura konektorów Claude
go nie wspiera, więc nie jest implementowany nawet jako fallback.

### stdio (lokalne debugowanie)

```bash
docker run -i --rm --env-file .env -e SYSTIM_TRANSPORT=stdio systim-mcp:dev
```

W tym trybie `stdout` należy wyłącznie do protokołu MCP, więc logi `slog`
są kierowane na `stderr`.

---

## Podpięcie do claude.ai

1. Wystaw serwer pod publicznym adresem **HTTPS**.
2. W claude.ai → Ustawienia → Konektory → **Dodaj custom connector**.
3. Podaj adres `https://twoj-adres/mcp`.
4. W „Advanced settings" wpisz `client_id` i `client_secret` z authentika
   (`systim-mcp-connector` oraz sekret ustawiony w blueprincie).
5. Przejdź flow logowania.

> **Claude łączy się z chmury Anthropic, a nie z urządzenia użytkownika.**
> Localhost, VPN i sieci firmowe nie zadziałają. Serwer musi być osiągalny
> z publicznego internetu.

---

## Rozwiązywanie problemów

### Konektor działa w przeglądarce, ale nie działa z Claude

**Sprawdź WAF.** Blokowanie ruchu wychodzącego Anthropic przez WAF (Cloudflare,
AWS WAF, ModSecurity) to jedna z częstszych przyczyn tej sytuacji. Testujesz
z własnej przeglądarki i wszystko działa, bo Twój adres IP nie jest blokowany —
a żądania z chmury Anthropic są odrzucane, zanim dotrą do kontenera.

Zajrzyj do logów WAF-a, a nie do logów tego serwera.

### `403` mimo poprawnej konfiguracji

Dwie możliwe przyczyny:

1. **Walidacja `Origin`.** Serwer odrzuca żądania z nieznanym `Origin` (ochrona
   przed DNS rebinding — wymóg specyfikacji MCP). Żądania **bez** tego nagłówka są
   przepuszczane, bo `Origin` ustawiają przeglądarki, a Claude łączy się po stronie
   serwera. Jeśli przed serwerem stoi proxy dokładające `Origin`, dopisz jego adres
   do `SYSTIM_DODATKOWE_ORIGINY`.
2. **Ochrona localhost w SDK.** Gdy reverse proxy stoi na tym samym hoście
   w `network_mode: host`, żądania przychodzą z `127.0.0.1` przy nielokalnym
   nagłówku `Host`, co SDK traktuje jako próbę DNS rebinding. Wtedy ustaw
   `SYSTIM_WYLACZ_OCHRONE_LOCALHOST=true`.

### Po dodaniu konektora Claude w ogóle nie przekierowuje do logowania

Najczęstsza przyczyna: **provider w authentiku ma pustą listę `grant_types`.**

W authentiku (co najmniej od 2026.5) `OAuth2Provider` ma jawne pole `grant_types`,
którego wartością **domyślną jest pusta lista**. Provider bez wpisanego grantu
odrzuca każde żądanie autoryzacji błędem `invalid_request` („The request is
otherwise malformed"), mimo że `client_id` i `redirect_uri` są poprawne.

Objaw myli z dwóch powodów:

- dokument discovery **i tak ogłasza** `authorization_code` i `refresh_token` —
  to statyczna lista instancji, a nie ustawienie tego providera;
- authentik przekierowuje na zarejestrowany `redirect_uri` z błędem w parametrach,
  więc z zewnątrz wygląda to jak poprawnie działający endpoint.

Rozpoznanie — w logach authentika pojawia się wtedy wprost:

```
"event": "Invalid grant_type for provider", "grant_type": "authorization_code"
```

Sprawdzenie z zewnątrz, bez dostępu do logów (podstaw swój `client_id` i issuer):

```bash
curl -si "https://auth.firma.pl/application/o/authorize/?client_id=systim-mcp-connector&redirect_uri=https%3A%2F%2Fclaude.ai%2Fapi%2Fmcp%2Fauth_callback&response_type=code&scope=openid&state=t" | grep -i '^location:'
```

- `Location: /if/flow/...` → poprawnie, provider kieruje do logowania,
- `Location: https://claude.ai/...?error=invalid_request` → brakuje `grant_types`.

Naprawa: dołączony blueprint ustawia już `grant_types: [authorization_code,
refresh_token]`. Zastosuj go ponownie albo uzupełnij pole ręcznie w authentiku
(Applications → Providers → `systim-mcp`).

Uwaga przy blueprintach: po zmianie pliku warto zrestartować kontener
`authentik-worker` — to on je stosuje, a wykrycie zmiany bywa opóźnione.

### Forma płatności wychodzi jako gotówka

**Dokumentacja Systim jest w tym miejscu nieaktualna.** Opisuje `forma_platnosci`
jako pole przyjmujące nazwę tekstową (`przelew`, `gotówka`, …), ale nazwa nie
odnosi żadnego skutku — API przyjmuje ją bez błędu i wstawia gotówkę.
**Pole oczekuje ID**, tak samo jak `stawka_vat`, `id_numeracji` i `id_szablonu`.

Sprawdzone na żywym koncie, wszystkie sześć form, po jednym dokumencie na każdą:

| Forma | Wysłane ID | Zapisane w Systim |
|---|---|---|
| przelew | 1 | **1** ✓ |
| gotówka | 2 | **2** ✓ |
| barter | 3 | **3** ✓ |
| za pobraniem | 4 | **4** ✓ |
| rozliczenie saldami | 5 | **5** ✓ |
| karta płatnicza | 6 | **6** ✓ |

Dla porównania: 20 wcześniejszych dokumentów wysłanych z **nazwą** dostało `2`
(gotówka) — bez wyjątku, niezależnie od tego, którą formę podano.

Serwer wysyła ID zawsze — wariant z nazwą nie jest wspierany, bo produkuje
dokumenty z błędną formą płatności. Jeśli Twoje konto ma inną kartotekę rodzajów
płatności, nadpisz ID przez `SYSTIM_FORMY_PLATNOSCI`; przy braku mapowania pole
nie jest wysyłane, a `przygotuj_fakture` o tym ostrzega.

Warto ustawić też domyślną formę, żeby pominięcie pola nie dawało gotówki:

```
SYSTIM_DOMYSLNA_FORMA_PLATNOSCI=przelew
```

Formę zapisaną na dokumencie pokazuje `lista_faktur` (jako ID z tabeli powyżej).

### „Błędne przypisanie rodzaju dokumentu do numeracji"

Seria numeracji wskazana w `SYSTIM_ID_NUMERACJI` jest w Systim przypisana do innego
typu dokumentu niż wystawiany `rodzaj`. Uzupełnij mapę o właściwy wpis, np. dla
pro formy:

```
SYSTIM_ID_NUMERACJI={"0":1,"1":5}
SYSTIM_ID_SZABLONU={"0":43,"1":1}
```

Sprawdź **oba** pola naraz — szablon podlega dokładnie tej samej regule i po
naprawieniu samej numeracji dokument potrafi zostać odrzucony ponownie.
ID odczytasz w panelu: Ustawienia → Numeracja dokumentów oraz Szablony wydruku,
kolumna `ID`.
Od wersji z mapowaniem brak wpisu dla danego rodzaju jest wykrywany już
w `przygotuj_fakture`, a więc **przed** nieodwracalnym zatwierdzeniem.

### `pobierz_pdf` — permission denied

Kontener działa jako UID 65532, a katalog na PDF-y należy do kogoś innego. Zdarza się
przy bind moncie z hosta — wtedy katalog ma właściciela z systemu gospodarza:

```bash
sudo chown -R 65532:65532 <katalog-na-hoscie>
```

Nazwany wolumen (jak w dołączonym `docker-compose.yml`) dziedziczy właściciela
z obrazu i tego problemu nie ma.

### Konektor działa kilkanaście minut, po czym się rozłącza

Prawie na pewno brak refresh tokenu. Authentik od wersji 2024.2 wydaje go tylko
wtedy, gdy klient poprosił o scope `offline_access`. Sprawdź dwie rzeczy:

1. czy `offline_access` jest w `property_mappings` providera w authentiku,
2. czy serwer ogłasza go w nagłówku:

```bash
curl -si -X POST https://mcp.firma.pl/mcp -H 'Content-Type: application/json' -d '{}' | grep -i www-authenticate
```

W parametrze `scope` powinny być `systim:faktury openid offline_access`.

### Pojedyncze `401` zaraz po restarcie serwera

W logach wygląda to sprzecznie: najpierw `pobrano zestaw kluczy z serwera
autoryzacji`, a ułamek milisekundy później `odrzucono token dostępowy` z powodem
„nieznany klucz podpisujący: zestaw kluczy odświeżano mniej niż 1m0s temu".

Był to błąd w `ZrodloKluczy`, naprawiony: żądania czekające na cudze pobranie
JWKS sprawdzały stan sprzed oczekiwania, więc trafiały w limit częstotliwości
odświeżeń zamiast skorzystać z kluczy, które właśnie się pojawiły. Objawiało się
przy zimnym starcie i przy rotacji kluczy, gdy przychodziło kilka żądań naraz —
konektor się podnosił po ponowieniu, ale użytkownik widział losowe rozłączenie.

Jeśli widzisz taką parę linii na starszej wersji, zaktualizuj serwer.

### `401` mimo poprawnego logowania — niezgodny `iss` albo `aud`

Dwa najczęstsze przypadki przy authentiku:

- **`iss`**: w trybie per-provider issuer zawiera slug aplikacji i kończy się
  ukośnikiem. `https://auth.firma.pl/` zamiast
  `https://auth.firma.pl/application/o/systim-mcp/` nie zadziała.
- **`aud`**: authentik wstawia tam `client_id`, więc `OIDC_AUDIENCE` musi być
  równe `systim-mcp-connector`, a nie adresowi serwera MCP.

Obie rzeczy potwierdzisz jednym poleceniem:

```bash
AUTH_ISSUER=https://auth.firma.pl/application/o/systim-mcp/ go test ./internal/auth/ -run TestIntegracjaZPrawdziwymIdP -v
```

Przy `LOG_LEVEL=debug` serwer loguje też powód odrzucenia każdego tokenu.

### Błąd 13 „brak sesji użytkownika"

To sytuacja **normalna, nie awaria**. Sesje API wygasają po czasie ustawionym
w opcjach konta, a **każde zalogowanie użytkownika do panelu WWW kasuje wszystkie
sesje API**. Klient przechwytuje ten błąd, loguje się ponownie i ponawia żądanie
dokładnie raz. Jeśli widzisz ten komunikat w odpowiedzi narzędzia, ponowienie też
się nie powiodło.

### Błąd 2 „dostęp zabroniony"

Zwykle throttling za zbyt intensywne odpytywanie API albo blokada po adresie IP.
Odczekaj i ogranicz liczbę wywołań.

### Błąd 16 „miesiąc jest zamknięty"

Okres księgowy, w którym miał powstać dokument, został zamknięty w Systim. Otwórz
go w panelu albo wystaw dokument z datą z bieżącego, otwartego miesiąca.

### `result_code: 102` po wystawieniu

Dokument **powstał i ma numer**, ale zapis w księgowości się nie udał. Narzędzie
zwraca to jako ostrzeżenie wymagające uwagi — trzeba poprawić księgowanie ręcznie
w panelu.

---

## Testy

```bash
go test -race ./...
go vet ./...
gofmt -l .
```

Testy używają wyłącznie `net/http/httptest` — bez zewnętrznych bibliotek
mockujących. Pokrywają między innymi:

- **Kształt JSON:** `error.code` jako liczba i jako string, `error.fields` jako
  tablica / mapa / brak, `result` jako mapa kluczowana ID i jako tablica, pole
  liczbowe przychodzące jako `""`, pusta tablica zamiast pustej mapy, encje HTML
  w nazwie kontrahenta.
- **Sesje:** błąd 13 → przelogowanie → ponowienie → sukces; błąd 13 dwa razy
  z rzędu → błąd bez pętli; 16 równoległych wywołań na wygasłym tokenie → **jedno**
  logowanie (pod `-race`).
- **Arytmetyka:** 3 × 33,33 zł przy 23%, kwoty trafiające dokładnie w połówkę
  grosza, oraz osobny test potwierdzający, że `decimal.Round` zaokrągla
  half-away-from-zero.
- **Żądanie `addSellInvoice`:** asercja, że w ciele są klucze `opis[0]`, `ilosc[0]`
  itd. w konwencji PHP, a nie JSON.
- **Plik logu:** rotacja po przekroczeniu rozmiaru, przesuwanie i kasowanie
  najstarszych kopii, wariant bez kopii, dopisywanie po restarcie z zachowaniem
  rozmiaru oraz 400 równoległych zapisów bez zgubionego bajtu (pod `-race`).
- **Cache kartotek:** kolejne odczyty idą z cache, po TTL kartoteka jest pobierana
  ponownie, 16 równoległych odczytów daje **jedno** pobranie (pod `-race`), brak
  trafienia wymusza dokładnie jedno odświeżenie, a `przygotuj_fakture` po
  `szukaj_kontrahenta` nie pyta API drugi raz.
- **Szkice:** poprawny przechodzi, ze zmienioną kwotą jest odrzucany,
  przeterminowany jest odrzucany, wygenerowany innym kluczem jest odrzucany.
- **Uwierzytelnianie:** `/mcp` bez tokenu → `401` z `WWW-Authenticate`; token
  z błędnym `aud`, po `exp`, z obcym `iss`, podpisany nieznanym kluczem, z `alg=none`
  i z `alg=HS256` → odrzucony; brak scope → `403`.
- **JWKS przy zimnym cache:** 8 równoległych żądań na pusty zestaw kluczy daje
  **jedno** pobranie i **żadnego** odrzuconego tokenu; nieznany `kid` nadal nie
  dobija IdP, a klucz obecny w cache przechodzi mimo aktywnego limitu odświeżeń.
- **Origin:** żądanie z obcym `Origin` → odrzucone; bez nagłówka → przepuszczone.
- **Scope:** `WWW-Authenticate` ogłasza `offline_access`, ale token bez niego nadal
  przechodzi — o `offline_access` prosi się serwer autoryzacji, a nie sprawdza w tokenie.

Osobno, poza `go test ./...`, można sprawdzić konfigurację prawdziwego IdP:

```bash
AUTH_ISSUER=https://auth.firma.pl/application/o/systim-mcp/ go test ./internal/auth/ -run TestIntegracjaZPrawdziwymIdP -v
```

Test jest domyślnie pomijany. Weryfikuje discovery, obecność PKCE S256 i to, czy
parser przyjmuje wszystkie klucze z JWKS — czyli dokładnie te rzeczy, które inaczej
objawiłyby się dopiero jako nieudane podpięcie konektora.

---

## Decyzje projektowe i odstępstwa

### Go 1.25 zamiast 1.24 w obrazie budującym

Wymuszone. `github.com/modelcontextprotocol/go-sdk` w wersji `v1.7.0-pre.3`
deklaruje w `go.mod` wymaganie `go 1.25.0`, więc `golang:1.24` nie zbuduje tego
projektu.

### SDK w wersji pre-release

**`v1.7.0` stabilne nie istnieje** — najnowsze stabilne to `v1.6.1`. Wersja
`v1.7.0-pre.3` została wybrana świadomie, bo wnosi `MaxRequestBodyBytes`
i `PropagateRequestCancellation` wprost w opcjach handlera. Na `v1.6.1` te dwa
wymagania trzeba by realizować własnym kodem (`http.MaxBytesHandler` i `BaseContext`);
sam tryb `Stateless` jest dostępny w obu wersjach.

### Szkice jako podpisane tokeny, a nie mapa w pamięci

Tryb `Stateless = true` jest wymagany przez najnowszą wersję protokołu w transporcie
streamable HTTP. Trzymanie szkiców w mapie chronionej mutexem — wraz z goroutine
sprzątającą wygasłe wpisy — zakładałoby, że kolejne żądanie trafi w ten sam proces,
co w trybie stateless nie jest prawdą. Dlatego cały stan szkicu jest przenoszony
wewnątrz podpisanego `szkic_id`, a TTL (30 minut) siedzi w payloadzie i jest
sprawdzany przy weryfikacji. Nie ma czego sprzątać, bo nic nie leży w RAM.

### Rabat jest stosowany po stronie serwera, a pole `rabat` nie jest wysyłane

API Systim nie liczy kwot, więc samo przesłanie pola `rabat` **nie obniżyłoby**
kwot na dokumencie. Serwer stosuje rabat do ceny jednostkowej każdej pozycji
i wysyła już obniżone kwoty.

Samego pola `rabat` **celowo nie przesyłamy**, mimo że API je przyjmuje. Powód
jest empiryczny, potwierdzony na żywym koncie:

> Dokument z rabatem i **trzema lub więcej pozycjami** przewraca backend Systim
> błędem PHP `Uncaught Error: Cannot assign an empty string to a string offset`.
> Odpowiedź nie jest wtedy JSON-em, dokument nie powstaje i nie zużywa numeru.
> Z dwiema pozycjami to samo żądanie przechodzi bez problemu.

Nic na tym nie tracimy — rabat jest już w cenach jednostkowych. Znika jedynie
osobna adnotacja o rabacie na wydruku, a przy okazji odpada ryzyko pokazania
rabatu dwukrotnie, gdyby szablon sam go odejmował.

### Waluty obce nie są zaimplementowane

Rodzaje `23`, `25`, `29`, `43` i `44` wymagają dodatkowo pól `waluta`,
`data_waluty`, `kurs_waluty` i `platnosc_walutowa`. Są **jawnie odrzucane**
z komunikatem wyjaśniającym, zamiast po cichu wystawiać niepoprawny dokument.

### Kwoty wyłącznie na `decimal.Decimal`

Nigdzie w ścieżce liczenia nie pojawia się `float64` — także na wejściu narzędzi,
gdzie ilość i cena są przyjmowane jako stringi. Kwoty w odpowiedziach też są
stringami z dwoma miejscami po przecinku, więc model widzi dokładnie te wartości,
które trafią na dokument.

### Rekordy kartotek są dekodowane elastycznie

Dokumentacja Systim nie podaje stabilnych nazw pól w metodach listujących. Zamiast
sztywnej struktury, która po cichu gubiłaby dane, każdy rekord jest spłaszczany do
mapy `nazwa → wartość`, a pola takie jak nazwa czy NIP są odczytywane z listy
kandydatów. Nowe albo inaczej nazwane pole nadal dociera do użytkownika.

---

### Kartoteki są cache'owane w pamięci procesu

Metody listujące Systim nie przyjmują parametru wyszukiwania, więc każde
wyszukanie kontrahenta czy produktu pobiera **całą kartotekę**. Bez cache jedna
faktura ściągała ją dwa razy: raz w `szukaj_kontrahenta`, drugi raz
w `przygotuj_fakture`, które odczytuje nazwę nabywcy do podglądu.

Cache żyje w pamięci procesu (`SYSTIM_CACHE_KARTOTEK`, domyślnie 5 minut) i nie
kłóci się z trybem `Stateless` — to pamięć podręczna odczytów, a nie stan sesji
MCP. Przy wielu replikach każda grzeje się osobno, co jest bez znaczenia, bo
chodzi wyłącznie o oszczędność wywołań.

Nieświeżość jest ograniczona z dwóch stron: TTL oraz **wymuszone odświeżenie przy
braku trafienia**. Kontrahent założony w panelu przed chwilą zostanie znaleziony
przy pierwszym wyszukaniu, a nie dopiero po wygaśnięciu wpisu. Świeżo pobrana
kartoteka bez szukanego rekordu nie powoduje kolejnego pobrania — wtedy rekordu
po prostu nie ma.

Faktury (`lista_faktur`) **nie są cache'owane**: to narzędzie służy do weryfikacji,
czy dokument faktycznie powstał, więc musi widzieć stan bieżący.

### Logi idą na stdout, a do pliku tylko na życzenie

Domyślnie logi trafiają na stdout i zbiera je Docker — `docker compose logs`
wystarcza, dopóki nie trzeba ich przeglądać po przebudowaniu kontenera albo
podać czemuś ścieżkę.

`SYSTIM_LOG_PLIK` włącza dodatkowy zapis do pliku. **Dodatkowy, nie zamienny**:
strumień standardowy zostaje, bo to z niego czyta platforma hostingowa i sam
`docker compose logs`. Ścieżka musi wskazywać na zamontowany wolumen — kontener
działa z `read_only: true` i poza wolumenami nie ma gdzie pisać. W dołączonym
`docker-compose.yml` służy do tego wolumen `logi` pod `/data/logi`, a katalog
istnieje już w obrazie, dzięki czemu wolumen dziedziczy właściciela 65532 i nie
trzeba go ręcznie `chown`ować.

Rotacja jest zrobiona w serwerze, a nie zostawiona logrotate: obraz jest
distroless, więc nie ma w nim ani powłoki, ani crona, które mogłyby plik obrócić.
Bez tego plik na wolumenie rósłby bez końca. Domyślne 10 MB × 3 kopie
odpowiadają ustawieniom sterownika `json-file` w `docker-compose.yml`, żeby oba
mechanizmy trzymały tyle samo historii.

> **`LOG_LEVEL=debug` loguje pełne ciało żądań do Systim** — nazwy pozycji, kwoty
> i ID kontrahenta. Hasło i token są maskowane, ale reszta to dane handlowe
> klientów, więc debug włączaj na czas diagnozy, a nie na stałe.

### Czas każdego wywołania API trafia do logów

Przy `LOG_LEVEL=debug` każde wywołanie Systim loguje `czas_ms` i rozmiar
odpowiedzi, a wywołanie powyżej 5 sekund jest zgłaszane ostrzeżeniem niezależnie
od poziomu logów. To jedyny sposób, żeby odróżnić wolne API Systim od wolnego
serwera, zanim zacznie się cokolwiek stroić.

## Struktura

```
mcpsystim/
├── cmd/systim-mcp/main.go     # wybór transportu, wiring, shutdown, sonda
├── internal/
│   ├── config/                # odczyt i walidacja env
│   ├── systim/                # klient API: token, retry, dekodowanie JSON
│   │   ├── client.go
│   │   ├── types.go           # typy znoszące zmienny kształt JSON
│   │   ├── rekord.go          # elastyczne dekodowanie rekordów kartotek
│   │   ├── errors.go
│   │   ├── metody.go          # metody listujące i PDF
│   │   └── invoice.go         # addSellInvoice i budowa tablic pozycji
│   ├── invoicing/             # przeliczenia decimal, stawki VAT, podpisane szkice
│   ├── auth/                  # resource server: JWKS, walidacja JWT, PRM, Origin
│   ├── logging/               # zapis logów do pliku z rotacją po rozmiarze
│   └── tools/                 # definicje narzędzi MCP
├── deploy/authentik/          # blueprint: provider OAuth2, aplikacja, scope
├── Dockerfile
├── docker-compose.yml
├── .env.example
└── README.md
```

## Licencja

Do ustalenia przez właściciela repozytorium.
