import * as esbuild from "https://deno.land/x/esbuild@v0.12.1/mod.js"
import { serve, Server, Response, ServerRequest, STATUS_TEXT } from "https://deno.land/std@0.97.0/http/mod.ts"
import * as path from "https://deno.land/std@0.97.0/path/mod.ts";


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
        return `document.body.innerHTML = \`<pre>${text.replaceAll("`", "\\`")}</pre>\`;export const Elm = {}`
    }

}

/* SERVER */
const cache = new Map()

const broadcastMap = new Set<ServerRequest>()
const broadcastReload = (filename: string) => {
    for (const req of broadcastMap) {
        try {
            void writeSSE(filename, req)
        } catch (err) {
            console.log("GOT ERROR", err)
        }
    }
}

const watch = async () => {
    const watcher = Deno.watchFs(rootDir)
    for await (const event of watcher) {
        event.paths.forEach((path) => {
            const value = cache.get(path)
            if (event.kind === "modify" && value) {
                cache.delete(path)
                broadcastReload(path)
            }
        })
    }
}

const fromCache = async (path: string, fn: (src: string) => Promise<string | Uint8Array>) => {
    let body = cache.get(path)
    if (!body) {
        body = await fn(path)
        cache.set(path, body)
        return body
    } else {
        return body
    }
}

const encoder = new TextEncoder()

// This is the header part taken from writeResponse
function encodeHeader(res: { status: number, headers: Headers }) {
    const protoMajor = 1
    const protoMinor = 1
    const statusCode = res.status || 200
    const statusText = STATUS_TEXT.get(statusCode)
    let out = `HTTP/${protoMajor}.${protoMinor} ${statusCode} ${statusText}\r\n`

    const headers = res.headers ?? new Headers()

    for (const [key, value] of headers) {
        out += `${key}: ${value}\r\n`
    }
    out += `\r\n`

    return encoder.encode(out)
}

// Writes and flushes the header
export async function setSSE(req: ServerRequest) {
    const res = {
        status: 200,
        headers: new Headers({
            Connection: "keep-alive",
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "Access-Control-Allow-Origin": "*",
        })
    }
    await req.w.write(encodeHeader(res))
    broadcastMap.add(req)
    req.done.then(() => broadcastMap.delete(req))
    return await req.w.flush()
}

// Writes and flushes an event
export async function writeSSE(filename: string, req: ServerRequest) {
    let result = `\ndata: ${filename}\n\n`
    try {
        await req.w.write(encoder.encode(result))
        await req.w.flush()
    } catch (err) {
    }
}

async function handle(server: Server) {
    const jsHeader = new Headers()
    jsHeader.append("Content-Type", "application/javascript")
    const body404 = await fromCache(`${rootDir}/404.html`, Deno.readFile)
    const indexHtml = await Deno.realPath(`${rootDir}/index.html`)
    for await (const req of server) {
        const t0 = performance.now()
        const url = req.url
        if (url === "/reload") {
            await setSSE(req)
            continue
        }
        let response: Response = { body: "" }
        if (url === "/") {
            try {
                cache.clear()
                response.body = await fromCache(indexHtml, Deno.readFile)
            } catch (e) {
                response.body = body404
                response.status = 404
            }
        } else {
            try {
                let p = `${rootDir}${url}`
                if (path.parse(p).ext === ""){
                    p += ".ts"
                }
                const realPath = await Deno.realPath(p)
                if (realPath.endsWith(".elm")) {
                    response.body = await fromCache(realPath, transformElm)
                    response.headers = jsHeader
                } else if (realPath.endsWith(".ts")) {
                    response.body = await fromCache(realPath, transformTS)
                    response.headers = jsHeader
                } else {
                    response.body = await fromCache(realPath, Deno.readFile)
                }
            } catch (e) {
                try {
                    response.body = await fromCache(`${rootDir}404.html`, Deno.readFile)
                    response.status = 404
                } catch (e) {
                    response.body = body404
                    response.status = 404
                }
            }
        }
        req.respond(response)
        const t1 = performance.now()
        console.log(`${t0}:Request "${url}" took ${t1 - t0}ms.`)
    }
}

void watch()
void open("http://localhost:8080/")
void handle(serve({ port: 8080 }))
