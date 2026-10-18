// Draws the Background picker's thumbnails: one small PNG per scene in frontend/src/components/ascii/scenes.ts,
// written to frontend/src/components/ascii/previews/<id>.png. Run it after adding or changing a scene and commit the PNGs:
//
//   bun scripts/gen-scene-previews.mjs            (needs `bun install --cwd frontend` for the ascii.rest package)
//   bun scripts/gen-scene-previews.mjs --check    exit 1 when a scene has no thumbnail
//
// A scene is a pure function of time that returns 200 x 100 dots (characters plus a palette index per cell). Each
// thumbnail is that picture at t = 0, the same frame the band shows while paused or with reduced motion, averaged down
// 2 x 2 cells to a pixel (dot size becomes coverage; the dot's colour is blended over the scene's ground). The picker
// shows them with `image-rendering: pixelated`, so no scene code runs when Settings opens. The PNG is written by hand
// (zlib from the standard library): no image dependency.
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { deflateSync } from 'node:zlib'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const ascii = join(root, 'frontend', 'src', 'components', 'ascii')
const outDir = join(ascii, 'previews')
const vendored = join(ascii, 'pieces')
const packaged = join(root, 'frontend', 'node_modules', 'ascii.rest', 'dist', 'pieces')
const check = process.argv.includes('--check')

const ids = [...readFileSync(join(ascii, 'scenes.ts'), 'utf8').matchAll(/id:\s*"([a-z0-9-]+)"/g)].map((m) => m[1])
if (ids.length === 0) throw new Error('no scenes found in scenes.ts')

const COVER = { ' ': 0, '·': 0.3, '•': 0.6, '●': 1 }
const SCALE = 2
const hex = (css) => [1, 3, 5].map((i) => parseInt(css.slice(i, i + 2), 16))

const CRC = new Uint32Array(256)
for (let n = 0; n < 256; n++) {
  let c = n
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
  CRC[n] = c >>> 0
}
const crc32 = (buf) => {
  let c = 0xffffffff
  for (const b of buf) c = CRC[(c ^ b) & 0xff] ^ (c >>> 8)
  return (c ^ 0xffffffff) >>> 0
}
const chunk = (type, data) => {
  const out = Buffer.alloc(12 + data.length)
  out.writeUInt32BE(data.length, 0)
  out.write(type, 4, 'latin1')
  data.copy(out, 8)
  out.writeUInt32BE(crc32(out.subarray(4, 8 + data.length)), 8 + data.length)
  return out
}

/** 8-bit RGB PNG, every row Paeth-filtered. */
function png(width, height, rgb) {
  const stride = width * 3
  const raw = Buffer.alloc((stride + 1) * height)
  for (let y = 0; y < height; y++) {
    raw[y * (stride + 1)] = 4
    for (let i = 0; i < stride; i++) {
      const a = i >= 3 ? rgb[y * stride + i - 3] : 0
      const b = y > 0 ? rgb[(y - 1) * stride + i] : 0
      const c = y > 0 && i >= 3 ? rgb[(y - 1) * stride + i - 3] : 0
      const p = a + b - c
      const pa = Math.abs(p - a), pb = Math.abs(p - b), pc = Math.abs(p - c)
      const pred = pa <= pb && pa <= pc ? a : pb <= pc ? b : c
      raw[y * (stride + 1) + 1 + i] = (rgb[y * stride + i] - pred) & 0xff
    }
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(width, 0)
  ihdr.writeUInt32BE(height, 4)
  ihdr[8] = 8
  ihdr[9] = 2
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ])
}

async function loadPiece(id) {
  for (const dir of [vendored, packaged]) {
    const file = join(dir, `${id}.js`)
    if (existsSync(file)) return import(pathToFileURL(file).href)
  }
  throw new Error(`scene ${id}: not in ${vendored} or the ascii.rest package`)
}

async function thumbnail(id) {
  const piece = await loadPiece(id)
  const { cols, rows, palette, ground } = piece.meta
  if (!palette || !ground) throw new Error(`scene ${id}: not a colour piece`)
  const color = new Uint8Array(cols * rows)
  const lines = piece.default({})(0, { color, paper: false }).split('\n')
  if (lines.length !== rows) throw new Error(`scene ${id}: ${lines.length} rows, expected ${rows}`)
  const pal = palette.map(hex)
  const bg = hex(ground)
  const w = cols / SCALE, h = rows / SCALE
  const rgb = Buffer.alloc(w * h * 3)
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) {
      const sum = [0, 0, 0]
      for (let dy = 0; dy < SCALE; dy++) {
        for (let dx = 0; dx < SCALE; dx++) {
          const cx = x * SCALE + dx, cy = y * SCALE + dy
          const cover = COVER[lines[cy][cx]] ?? 0.6
          const ink = pal[color[cy * cols + cx]] ?? bg
          for (let k = 0; k < 3; k++) sum[k] += bg[k] * (1 - cover) + ink[k] * cover
        }
      }
      for (let k = 0; k < 3; k++) rgb[(y * w + x) * 3 + k] = Math.min(255, Math.round(sum[k] / (SCALE * SCALE)))
    }
  }
  return png(w, h, rgb)
}

mkdirSync(outDir, { recursive: true })
let missing = 0
let total = 0
for (const id of ids) {
  const file = join(outDir, `${id}.png`)
  if (check) {
    if (!existsSync(file)) {
      missing++
      console.error(`scene-previews: ${id}.png is missing`)
    }
    continue
  }
  const bytes = await thumbnail(id)
  total += bytes.length
  const same = existsSync(file) && readFileSync(file).equals(bytes)
  if (!same) writeFileSync(file, bytes)
  console.log(`${id}.png ${(bytes.length / 1024).toFixed(1)} KiB${same ? ' (unchanged)' : ''}`)
}
if (!check) console.log(`${ids.length} scenes, ${(total / 1024).toFixed(0)} KiB of thumbnails`)
if (missing) process.exit(1)
