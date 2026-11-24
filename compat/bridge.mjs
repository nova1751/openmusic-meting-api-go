import { timingSafeEqual, createHash } from 'node:crypto'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { serve } from '@hono/node-server'
import { Hono } from 'hono'

const directory = process.env.DATA_DIR || './data'
const bridgeKey = process.env.LEGACY_BRIDGE_KEY || ''
if (bridgeKey && (bridgeKey.length < 32 || bridgeKey.trim() !== bridgeKey)) {
    throw new Error('LEGACY_BRIDGE_KEY must contain at least 32 characters')
}
if (!existsSync(join(directory, 'users.json')) &&
    (!process.env.ADMIN_PASSWORD || process.env.ADMIN_PASSWORD === 'admin123')) {
    throw new Error('Set a private ADMIN_PASSWORD before creating a new data directory')
}

const [{ default: app }, { default: store }, { apiTokenMiddleware }] = await Promise.all([
    import('./app.js'),
    import('./src/admin/store.js'),
    import('./src/middleware/auth.js'),
])

const bridgePath = '/__meting_go'
const hash = value => createHash('sha256').update(value).digest()
const expected = bridgeKey ? hash(bridgeKey) : null
const router = new Hono()
router.use('/api', apiTokenMiddleware, c => {
    const explicit = c.req.header('X-OpenMusic-Cookie')?.trim()
    const cookie = explicit || store.getActiveCookie('netease')?.cookie || ''
    return c.json({ cookie })
})

function fetch(request) {
    const path = new URL(request.url).pathname
    if (path !== bridgePath && !path.startsWith(bridgePath + '/')) return app.fetch(request)
    if (path !== bridgePath + '/authorize' || request.method !== 'GET' ||
        !expected || !timingSafeEqual(expected, hash(request.headers.get('X-Meting-Bridge-Key') || ''))) {
        return new Response(null, { status: 404 })
    }
    const encoded = request.headers.get('X-Meting-Bridge-Target') || ''
    if (!/^[A-Za-z0-9_-]{1,24000}$/.test(encoded)) {
        return new Response(null, { status: 400 })
    }
    const rawTarget = Buffer.from(encoded, 'base64url').toString('utf8')
    if (rawTarget !== '/api' && !rawTarget.startsWith('/api?')) {
        return new Response(null, { status: 400 })
    }
    const headers = new Headers()
    for (const name of ['Authorization', 'X-OpenMusic-Assertion', 'X-OpenMusic-Signature', 'X-OpenMusic-Cookie']) {
        const value = request.headers.get(name)
        if (value) headers.set(name, value)
    }
    return router.fetch(new Request(new URL(rawTarget, 'http://localhost'), { headers }))
}

serve({ fetch, port: Number(process.env.PORT || 3000), hostname: process.env.COMPAT_LISTEN_HOST || '0.0.0.0' })
