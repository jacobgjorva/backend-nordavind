# Nordavind — backend

OpenAI-kompatibelt API-lag i Go. Ruter chat-forespørsler til EU-hostede open-weight-modeller og streamer SSE-svar tilbake til frontend.

## Oppsett

```sh
cp .env.example .env   # fyll inn Scaleway-nøkkel
go run ./cmd/server    # http://localhost:8080
```

Frontend peker `VITE_API_BASE_URL` mot `http://localhost:8080/v1`.

## Struktur

```
cmd/server/       main
internal/config/  miljøvariabler
internal/api/     HTTP-server, proxy med SSE-flush, CORS
```
