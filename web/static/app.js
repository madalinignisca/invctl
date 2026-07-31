// Alpine components, registered rather than written inline.
//
// This uses the CSP build of Alpine, so expressions in x-data are component
// names looked up here rather than JavaScript evaluated from an attribute.
// That is what lets the Content-Security-Policy stay at script-src 'self'
// with no 'unsafe-eval' -- the standard Alpine build compiles attribute
// strings with the Function constructor and would need it.
//
// Everything here is local UI state: disclosure, selection, submit feedback.
// Nothing fetches, and nothing holds domain state -- that is HTMX's job.

document.addEventListener('alpine:init', () => {
  // A collapsible section. `open` starts from the x-data argument so a panel
  // can render expanded on a page where it is the point.
  Alpine.data('disclosure', (open = false) => ({
    open: open,
    toggle() {
      this.open = !this.open;
    },
    get label() {
      return this.open ? 'Hide' : 'Show';
    },
  }));

  // Endpoint form: a unix socket has a path and no port, everything else is
  // the other way round. The server enforces this; the form just stops the
  // operator filling in a field that is about to be rejected.
  Alpine.data('endpointForm', () => ({
    proto: 'tcp',
    // Read the initial value off the select rather than assuming tcp. The form
    // now also renders pre-filled -- correcting an existing endpoint, and
    // re-rendering after a 422 -- and a unix socket opened for editing showed
    // the port field and hid the path it actually has.
    init() {
      const proto = this.$el.querySelector('select[name="l4_proto"]');
      if (proto) {
        this.proto = proto.value;
      }
    },
    get isUnix() {
      return this.proto === 'unix';
    },
    // The complement as its own getter, because the CSP build cannot evaluate
    // `!isUnix` in an attribute -- it warns and the directive does nothing, so
    // the port field stayed visible for a unix socket for as long as this form
    // has existed.
    get notUnix() {
      return !this.isUnix;
    },
    setProto(event) {
      this.proto = event.target.value;
    },
  }));

  // Dependency form: tolerance_seconds is required for an async edge and
  // meaningless otherwise, and the provider is either an endpoint or a route.
  Alpine.data('dependencyForm', () => ({
    nature: 'hard',
    providerKind: 'endpoint',
    get needsTolerance() {
      return this.nature === 'async';
    },
    get usesRoute() {
      return this.providerKind === 'route';
    },
    // Same reason as endpointForm.notUnix: a negation is an expression.
    get notRoute() {
      return !this.usesRoute;
    },
    setNature(event) {
      this.nature = event.target.value;
    },
    setProviderKind(event) {
      this.providerKind = event.target.value;
    },
  }));

  // Service form: min_healthy only means something for active_active, and
  // failover_mode only for active_passive.
  Alpine.data('serviceForm', (availability = 'standalone') => ({
    availability: availability,
    get needsMinHealthy() {
      return this.availability === 'active_active';
    },
    get needsFailover() {
      return this.availability === 'active_passive';
    },
    setAvailability(event) {
      this.availability = event.target.value;
    },
  }));

  // The neighbourhood diagram's zoom and pan.
  //
  // Local UI state and nothing else: a scale factor and a drag origin. It
  // fetches nothing and it holds no fact about the estate -- the picture is
  // server-rendered SVG and the layer toggles are a round trip on purpose,
  // because hiding a band client-side would leave a hole in the layout and a
  // viewBox describing a picture that is no longer drawn.
  //
  // scale 0 means "fit to the pane", which is the initial state: the SVG
  // already fits by CSS, so binding no width at all avoids a visible jump the
  // moment Alpine wakes up. Zooming starts from whatever is actually on screen.
  Alpine.data('diagramZoom', () => ({
    scale: 0,
    natural: 0,
    dragging: false,
    originX: 0,
    originY: 0,
    fromLeft: 0,
    fromTop: 0,
    init() {
      this.natural = parseFloat(this.$el.dataset.width) || 0;
    },
    get svgStyle() {
      if (!this.scale || !this.natural) {
        return '';
      }
      return 'width:' + Math.round(this.natural * this.scale) + 'px;max-width:none';
    },
    get zoomLabel() {
      return this.scale ? Math.round(this.scale * 100) + '%' : 'fit';
    },
    fitScale() {
      const pane = this.$refs.pane;
      if (!pane || !this.natural) {
        return 1;
      }
      return Math.min(1, pane.clientWidth / this.natural);
    },
    zoomIn() {
      this.scale = Math.min((this.scale || this.fitScale()) * 1.25, 4);
    },
    zoomOut() {
      this.scale = Math.max((this.scale || this.fitScale()) / 1.25, 0.25);
    },
    reset() {
      this.scale = 0;
    },
    panStart(event) {
      // A drag that starts on a node is that node's link being clicked, not a
      // pan. Only empty canvas moves the picture.
      if (event.target.closest('a')) {
        return;
      }
      const pane = this.$refs.pane;
      if (!pane) {
        return;
      }
      this.dragging = true;
      this.originX = event.clientX;
      this.originY = event.clientY;
      this.fromLeft = pane.scrollLeft;
      this.fromTop = pane.scrollTop;
    },
    panMove(event) {
      if (!this.dragging) {
        return;
      }
      const pane = this.$refs.pane;
      pane.scrollLeft = this.fromLeft - (event.clientX - this.originX);
      pane.scrollTop = this.fromTop - (event.clientY - this.originY);
    },
    panEnd() {
      this.dragging = false;
    },
  }));

  // Confirms a destructive action before its form submits. Retiring is
  // reversible in principle -- nothing is ever deleted -- but it still
  // deserves a deliberate second press.
  // The help drawer. A component rather than an inline expression because the
  // CSP build of Alpine is what `script-src 'self'` requires, and it evaluates
  // NO expressions -- only registered components, method references and
  // property getters. `x-data="{ open: false }"` and `x-on:click="open = true"`
  // are silently inert under it, which is exactly how this drawer shipped
  // doing nothing at all.
  Alpine.data('helpDrawer', () => ({
    open: false,
    show() {
      this.open = true;
    },
    hide() {
      this.open = false;
    },
    // Bound to the aside's class. A getter, not a ternary in the attribute.
    get panelClass() {
      return this.open ? 'is-open' : '';
    },
  }));

  Alpine.data('confirmAction', (message = 'Are you sure?') => ({
    confirm(event) {
      if (!window.confirm(message)) {
        event.preventDefault();
      }
    },
  }));
});

// A 422 carries a re-rendered form and must be swapped in.
//
// HTMX 2 does not swap a 4xx by default, which quietly defeats the convention
// this codebase uses everywhere: a validation failure returns 422 with the form
// partial re-rendered and error state on the offending field (CLAUDE.md, "HTTP
// and HTMX conventions"). Without this the server does the right thing and the
// operator sees nothing happen at all -- the worst of both, because the form
// still holds what they typed and gives no hint why it was refused.
//
// Deliberately only 422. A 400, 403 or 500 is not a form to re-render, and
// swapping one of those into a panel would put an error page inside a table.
document.addEventListener('htmx:beforeSwap', (event) => {
  if (event.detail.xhr && event.detail.xhr.status === 422) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
});

// Flash messages dismiss themselves. They are confirmations of something the
// operator just did, not information they need to act on.
document.addEventListener('DOMContentLoaded', () => {
  scheduleFlashDismissal(document);
});

document.addEventListener('htmx:afterSwap', (event) => {
  scheduleFlashDismissal(event.target.ownerDocument || document);
});

function scheduleFlashDismissal(root) {
  root.querySelectorAll('.flash:not([data-dismissing])').forEach((el) => {
    el.setAttribute('data-dismissing', 'true');
    setTimeout(() => el.remove(), 6000);
  });
}
