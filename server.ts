import * as esbuild from "https://deno.land/x/esbuild@v0.12.1/mod.js"
import * as ws from "https://deno.land/std/ws/mod.ts"
import { serve } from "https://deno.land/std/http/mod.ts"

const reloadWS = () => {
    const allConnections: ws.WebSocket[] = []
    // TODO Replace with EventSource https://developer.mozilla.org/en-US/docs/Web/API/EventSource
    void (async () => {
        for await(const req of serve(':5000')) {
            const wconn = { conn: req.conn, bufReader: req.r, bufWriter: req.w, headers: req.headers }
            const socket = await ws.acceptWebSocket(wconn)
            const i = allConnections.push(socket) - 1
            try {
                for await (const ev of socket) {
                    // wait for close
                }
            } catch (e) {
            }
            allConnections.splice(i, 1)
        }
    })()
    return (msg: string) => allConnections.forEach(s => s.send(msg))
}
const broadcastReload = reloadWS()

export async function open(url: string): Promise<void> {
    const programAliases = {
        windows: "explorer",
        darwin: "open",
        linux: "sensible-browser",
    }
    const process = Deno.run({ cmd: [programAliases[Deno.build.os], url] })
    await process.status()
}


/* OPTIONS */
const tempDirName0 = await Deno.makeTempDir()
const rootDir = "./src"

/* File Transform */
const transformTS = async (input: string) => {
    const data = await Deno.readTextFile(input)
    try {
        const result = await esbuild.transform(data, { loader: 'ts', })
        return result.code
    } catch (e) {
        return e
    }
}
const decoder = new TextDecoder("utf-8")
const transformElm = async (input: string) => {
    const outputFile = `${tempDirName0}/index.js`
    const p = Deno.run({
        cmd: ["elm", "make", input, `--output=${outputFile}`],
        stderr: "piped",
        stdout: "piped",
    })
    if ((await p.status()).success) {
        const data = await Deno.readTextFile(outputFile)
        p.close()
        return `
const scope = {};
${data.replace("}(this));", "}(scope));")}
export const { Elm } = scope;
`
    } else {
        p.close()
        // TODO find way how to keep colors
        const text = decoder.decode(await p.stderrOutput())
        console.log(`\n`)
        console.log(text)
        console.log(`\n`)
        return `document.body.innerHTML = \`<pre>${text.replaceAll("`","\\`")}</pre>\`;export const Elm = {}`
    }

}

/* SERER */
const cache = new Map()

const watch = async () => {
    const watcher = Deno.watchFs(rootDir)
    for await (const event of watcher) {
        event.paths.forEach((path) => {
            const value = cache.get(path)
            if (event.kind === "modify" && value) {
                cache.set(path, { body: value.body, dirty: true })
                broadcastReload(path)
            }
        })
    }
}

const fromCache = async (path: string, fn: (src: string) => Promise<string | Uint8Array>) => {
    let body = cache.get(path)
    if (!body || body.dirty) {
        body = await fn(path)
        cache.set(path, { body, dirty: false })
        return body
    } else {
        return body.body
    }

}

async function handle(conn: Deno.Conn) {
    const jsHeader = new Headers()
    jsHeader.append("Content-Type", "application/javascript")
    const body404 = await fromCache(`${rootDir}/404.html`, Deno.readFile)
    const indexHtml = await Deno.realPath(`${rootDir}/index.html`)
    for await (const req of Deno.serveHttp(conn)) {
        const t0 = performance.now()
        const url = new URL(req.request.url)
        if (url.pathname === "/reload") {
            console.log(req)
            const headers = req.request.headers
            await req.respondWith(new Response(body404, { status: 404 }))
            continue
        }
        let response: Response = new Response("")
        if (url.pathname === "/") {
            try {
                response = new Response(await fromCache(indexHtml, Deno.readFile))
            } catch (e) {
                response = new Response(body404, { status: 404 })
            }
        } else {
            try {
                const realPath = await Deno.realPath(`${rootDir}${url.pathname}`)
                if (realPath.endsWith(".elm")) {
                    response = new Response(await fromCache(realPath, transformElm), { headers: jsHeader })
                } else if (realPath.endsWith(".ts")) {
                    response = new Response(await fromCache(realPath, transformTS), { headers: jsHeader })
                }
            } catch (e) {
                try {
                    response = new Response(await fromCache(`${rootDir}404.html`, Deno.readFile), { "status": 404 })
                } catch (e) {
                    response = new Response(body404, { status: 404 })
                }
            }
        }
        try {
            await req.respondWith(response)
        } catch (e) {
        }

        const t1 = performance.now()
        console.log(`${t0}:Request "${url.pathname}" took ${t1 - t0}ms.`)
    }
}

const server = Deno.listen({ port: 8080 })
void watch()
void open("http://localhost:8080/")

for await (const conn of server) {
    void handle(conn)
}
