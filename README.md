### Auto Reload
To enable auto-reload functionality, add somewhere in code:
```javascript
    // Auto-reload
    (new EventSource('/reload')).onmessage = location.reload.bind(location)
```

### Dev-Server

Deno server
```bash
deno run --allow-net --allow-read --allow-run --allow-write --allow-env devserver.ts
```

Go server
```bash
go run devserver.go
```
