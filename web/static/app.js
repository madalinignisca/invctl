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
    get isUnix() {
      return this.proto === 'unix';
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

  // Confirms a destructive action before its form submits. Retiring is
  // reversible in principle -- nothing is ever deleted -- but it still
  // deserves a deliberate second press.
  Alpine.data('confirmAction', (message = 'Are you sure?') => ({
    confirm(event) {
      if (!window.confirm(message)) {
        event.preventDefault();
      }
    },
  }));
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
