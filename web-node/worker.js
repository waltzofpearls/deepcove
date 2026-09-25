// Serves the web node's WASM file from R2. Everything else is a static file and never reaches this.
export default {
  async fetch(request, env) {
    const key = new URL(request.url).pathname.slice(1);
    const object = await env.WASM.get(key);
    if (!object) {
      return new Response('Not found', { status: 404 });
    }
    return new Response(object.body, {
      headers: {
        'Content-Type': 'application/wasm',
        // The name carries a hash of the contents, so what is under it never changes.
        'Cache-Control': 'public, max-age=31536000, immutable',
        ETag: object.httpEtag,
      },
    });
  },
};
