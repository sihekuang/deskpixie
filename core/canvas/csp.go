package canvas

// CSP is the Content-Security-Policy for every canvas:// response (spec section 5). Single
// source of truth. 'unsafe-inline' is needed because pages carry their chart configs inline;
// there is no 'unsafe-eval', no network (connect-src 'none') and no frames, forms or <base>.
const CSP = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"
