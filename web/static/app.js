/*
 * invctl — infrastructure inventory
 * Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
 *
 * Licensed under the GNU Affero General Public License, version 3 only —
 * no later version applies. See LICENSE for the full text.
 *
 * SPDX-License-Identifier: AGPL-3.0-only
 */

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

// Only one disclosure menu in a toolbar may be open at a time.
//
// The Views menu (WP-G4b) and the Columns menu (WP-G4c) sit side by side and
// both anchor to the same left edge, so with independent `open` flags opening
// the second drew it straight over the first: two menus visible, overlapping,
// with the second's contents on top of the first's. Reported from the demo.
//
// Module scope rather than an Alpine store because it is not state any
// template renders -- no x-show or x-text reads it. It is a latch between two
// components, and a store would invite a template to bind to it.
let openDisclosure = null;

// claimDisclosure closes whatever else was open and records the new holder.
// The isConnected check matters because the whole #asset-table fragment is
// replaced on every filter keystroke: without it this would keep a reference
// to a component whose element is long gone, and the first menu opened after
// a filter change would "close" a corpse instead of the menu actually on
// screen.
function claimDisclosure(c) {
  if (openDisclosure && openDisclosure !== c) {
    const el = openDisclosure.$el;
    if (!el || el.isConnected) {
      openDisclosure.open = false;
    }
  }
  openDisclosure = c;
}

function releaseDisclosure(c) {
  if (openDisclosure === c) {
    openDisclosure = null;
  }
}

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

  // The navigation rail's collapsible sections.
  //
  // WHAT THE SERVER DECIDES AND WHAT THIS DOES. The group holding the current
  // page arrives already open, because the server knows which page it just
  // rendered and guessing that here would mean parsing the URL in two places.
  // Everything after the first paint is the operator's: what they opened or
  // closed is remembered, and it outranks the server's default on every later
  // visit, because a section somebody deliberately shut should stay shut.
  //
  // The current section is the exception -- it always opens, even if it was
  // collapsed last time. Landing on a page whose rail entry is hidden is
  // disorienting in a way that no amount of remembered state justifies.
  //
  // Arguments come through data-* rather than x-data("..."), because this is
  // Alpine's CSP build: attribute values are property and method names, never
  // expressions. diagramZoom reads data-width the same way.
  Alpine.data('navGroup', () => ({
    open: false,
    key: '',
    init() {
      this.key = this.$el.dataset.group || '';
      const current = this.$el.dataset.current === 'true';
      if (current) {
        // Open, and DELIBERATELY NOT REMEMBERED. "You are here" is a fact about
        // this one render; what the operator collapsed is a preference. Writing
        // the first into the store for the second meant a single visit to a
        // page left its group expanded on every page afterwards -- so a rail
        // somebody had tidied slowly un-tidied itself, with no way to stop it
        // short of never opening that section again.
        this.open = true;
        return;
      }
      const saved = this.recall();
      this.open = saved === null ? this.$el.dataset.open === 'true' : saved;
    },
    toggle() {
      this.open = !this.open;
      this.remember();
    },
    // Storage is best-effort. A browser with it disabled, or a private window
    // that throws on write, gets a rail that works and forgets -- which is a
    // far better failure than a navigation that throws on every page.
    remember() {
      try {
        window.localStorage.setItem('invctl.nav.' + this.key, this.open ? '1' : '0');
      } catch (e) {
        /* ignore */
      }
    },
    recall() {
      try {
        const v = window.localStorage.getItem('invctl.nav.' + this.key);
        return v === null ? null : v === '1';
      } catch (e) {
        return null;
      }
    },
    get caretClass() {
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

  // The ownership report's bulk-assignment groups (WP-G7 piece 3,
  // docs/ownership-report-design.md §6). Local UI state only: which
  // checkboxes are ticked and which team is picked, so the confirm message
  // can name a count and a target before the request ever leaves the
  // browser. It fetches nothing itself -- the ids it reads are whatever
  // OwnershipCandidates most recently rendered into this group, which is
  // what makes "select all" apply to the CURRENT FILTERED VIEW rather than
  // to every unowned row of this type: there is no unfiltered list for it to
  // reach past.
  //
  // entityLabel comes through data-entity-label, not x-data(...): the CSP
  // build's x-data is a bare component name, the same reason diagramZoom
  // reads data-width instead of taking an argument.
  Alpine.data('bulkAssign', () => ({
    entityLabel: 'items',
    selected: 0,
    teamValue: '',
    teamLabel: '',
    init() {
      this.entityLabel = this.$el.dataset.entityLabel || 'items';
      this.updateSelected();
      this.updateTeamLabel();
    },
    checkboxes() {
      return this.$el.querySelectorAll('input[name="ids"]');
    },
    updateSelected() {
      this.selected = this.$el.querySelectorAll('input[name="ids"]:checked').length;
    },
    updateTeamLabel() {
      const select = this.$el.querySelector('select[name="team_id"]');
      if (!select || select.selectedIndex < 0) {
        this.teamValue = '';
        this.teamLabel = '';
        return;
      }
      this.teamValue = select.value;
      this.teamLabel = select.options[select.selectedIndex].textContent;
    },
    // "Select all IN THE CURRENT FILTERED VIEW" (design §6) -- this toggles
    // exactly the checkboxes present in the DOM right now, which is exactly
    // whatever OwnershipCandidates rendered for the active filter. There is
    // no broader set for it to reach past.
    selectAll() {
      this.checkboxes().forEach((cb) => {
        cb.checked = true;
      });
      this.updateSelected();
    },
    clearAll() {
      this.checkboxes().forEach((cb) => {
        cb.checked = false;
      });
      this.updateSelected();
    },
    get canSubmit() {
      return this.selected > 0 && this.teamValue !== '';
    },
    get disableSubmit() {
      return !this.canSubmit;
    },
    get confirmMessage() {
      const team = this.teamLabel || 'the selected team';
      return 'Assign ' + this.selected + ' ' + this.entityLabel + ' to ' + team + '?';
    },
  }));

  // WP-G4a piece 3's bulk-tag-apply panel (docs/tags-design.md §4a), the tag
  // twin of bulkAssign above -- same shape, same reasoning: local UI state
  // only, "select all" toggles exactly the checkboxes present in this
  // filtered view (there is no broader set for it to reach past), and the
  // confirm message names a count and a tag before the request leaves the
  // browser. Kept as its own component rather than parameterising bulkAssign
  // over a field name, since the two screens' targets (a team, a tag) read
  // and confirm differently enough that sharing one object would mean more
  // conditionals than code saved.
  Alpine.data('bulkTagApply', () => ({
    entityLabel: 'items',
    selected: 0,
    tagValue: '',
    tagLabel: '',
    init() {
      this.entityLabel = this.$el.dataset.entityLabel || 'items';
      this.updateSelected();
      this.updateTagLabel();
    },
    checkboxes() {
      return this.$el.querySelectorAll('input[name="entity"]');
    },
    updateSelected() {
      this.selected = this.$el.querySelectorAll('input[name="entity"]:checked').length;
    },
    updateTagLabel() {
      const select = this.$el.querySelector('select[name="tag_id"]');
      if (!select || select.selectedIndex < 0) {
        this.tagValue = '';
        this.tagLabel = '';
        return;
      }
      this.tagValue = select.value;
      this.tagLabel = select.options[select.selectedIndex].textContent;
    },
    selectAll() {
      this.checkboxes().forEach((cb) => {
        cb.checked = true;
      });
      this.updateSelected();
    },
    clearAll() {
      this.checkboxes().forEach((cb) => {
        cb.checked = false;
      });
      this.updateSelected();
    },
    get canSubmit() {
      return this.selected > 0 && this.tagValue !== '';
    },
    get disableSubmit() {
      return !this.canSubmit;
    },
    get confirmMessage() {
      const tag = this.tagLabel || 'the selected tag';
      return 'Apply ' + tag + ' to ' + this.selected + ' ' + this.entityLabel + '?';
    },
  }));

  // Column visibility for a wide list table, remembered per browser.
  //
  // STORES THE HIDDEN KEYS, NOT THE VISIBLE ONES, and that is the whole
  // design (docs/table-configs-design.md §3). If it stored what to show, a
  // release that adds a column would hide it from everyone who had ever
  // touched this menu -- they would never see the new field and would have no
  // reason to suspect one existed. Storing what to hide makes a new column
  // visible by default and makes a stale key naming a removed column inert.
  //
  // The table key arrives as data-table-key and is read via this.$el.dataset.tableKey
  // in init(), so one component serves all four tables without knowing anything about them.
  Alpine.data('columnPicker', () => ({
    table: '',
    hidden: [],
    open: false,

    init() {
      // The table key arrives as a data attribute, not an x-data argument:
      // this is Alpine's CSP build, where x-data is a component NAME and is
      // never evaluated, so `columnPicker('asset')` would silently leave
      // this empty -- same pattern as bulkTagApply's data-entity-label.
      // Must run before read()/apply(), which both depend on this.table.
      this.table = this.$el.dataset.tableKey || '';
      this.hidden = this.read();
      this.apply();
    },

    // localStorage throws in some privacy modes rather than returning null,
    // so every access is guarded. A browser that refuses to store simply
    // shows every column, which is the correct degraded state.
    read() {
      try {
        const raw = window.localStorage.getItem('invctl.cols.' + this.table);
        const parsed = raw ? JSON.parse(raw) : [];
        return Array.isArray(parsed) ? parsed.filter((k) => typeof k === 'string') : [];
      } catch (e) {
        return [];
      }
    },

    save() {
      try {
        window.localStorage.setItem('invctl.cols.' + this.table, JSON.stringify(this.hidden));
      } catch (e) {
        // Nothing to do: the preference is a convenience, not state anything
        // depends on.
      }
    },

    apply() {
      const root = this.$root;
      // Scoped to `table [data-col]`, not `[data-col]` alone: $root is the
      // wrapper div that contains BOTH the <table> and this component's own
      // picker partial, and the picker's checkboxes also carry data-col (so
      // their own value can travel to toggleColumn without an argument --
      // see that method's comment). An unscoped selector put col-hidden on
      // the checkbox itself, not just its table cell: unchecking "serial"
      // made the "serial" checkbox disappear from the menu along with the
      // column, leaving an orphan label and no way back except "Show all".
      // Chose the table-tag selector over `[data-table] [data-col]` because
      // it says directly what it excludes (the picker) rather than relying
      // on data-table being present and correctly placed on every table.
      root.querySelectorAll('table [data-col]').forEach((cell) => {
        cell.classList.toggle('col-hidden', this.hidden.indexOf(cell.dataset.col) !== -1);
      });
      document.querySelectorAll('.column-picker[data-columns="' + this.table +
        '"] input[data-col]').forEach((box) => {
        box.checked = this.hidden.indexOf(box.dataset.col) === -1;
      });
    },

    toggleMenu() {
      this.open = !this.open;
      if (this.open) {
        claimDisclosure(this);
      } else {
        releaseDisclosure(this);
      }
    },

    // Called by x-on:click.outside on .column-picker -- deliberately that
    // element and not $root. $root here is the wrapper that also contains the
    // whole table, so a click on any row would count as "inside" and the menu
    // would never close.
    close() {
      if (this.open) {
        this.open = false;
        releaseDisclosure(this);
      }
    },

    // x-on hands the event to the method; the key rides on the checkbox as
    // data-col, because the CSP build cannot pass an argument from an
    // attribute expression.
    toggleColumn(event) {
      const key = event.target.dataset.col;
      if (!key) {
        return;
      }
      const at = this.hidden.indexOf(key);
      if (at === -1) {
        this.hidden.push(key);
      } else {
        this.hidden.splice(at, 1);
      }
      this.save();
      this.apply();
    },

    isVisible(key) {
      return this.hidden.indexOf(key) === -1;
    },

    showAll() {
      this.hidden = [];
      this.save();
      this.apply();
    },
  }));

  // The Views menu. Local disclosure state only -- the views themselves are
  // server state and arrive rendered; this component does not fetch, does
  // not know a view's name or filters, and never should (see the file
  // header). data-views (the entity, "asset" or "service") is read by the
  // partial itself for form fields; this component only needs open/closed.
  Alpine.data('savedViews', () => ({
    open: false,

    init() {
      // A $watch, not a check inside toggleMenu/close alone: claimDisclosure
      // (module scope, above) can set `this.open = false` directly on a menu
      // that loses the latch to a sibling opening -- e.g. Columns opening
      // while Views is mid-rename -- bypassing both of this component's own
      // methods. Without this watcher a row's rename form stays open in the
      // DOM under a menu that is now `x-show`n hidden, and the NEXT time
      // this same component instance reopens (toggleMenu, no fresh element
      // -- the fragment was not replaced) the rename form reappears already
      // open for a row nobody clicked. Watching open itself catches every
      // path that flips it, not just the two this file writes today.
      this.$watch('open', (isOpen) => {
        if (!isOpen) {
          this.closeAnyRename();
        }
      });
    },

    toggleMenu() {
      this.open = !this.open;
      if (this.open) {
        claimDisclosure(this);
      } else {
        releaseDisclosure(this);
      }
    },
    // Called by x-on:click.outside on the partial's root, which wraps the
    // button as well as the menu -- so clicking the button itself is NOT
    // outside, and does not fight the toggle above.
    close() {
      if (this.open) {
        this.open = false;
        releaseDisclosure(this);
      }
    },

    // Which row's rename form shows is plain DOM state (a CSS class on the
    // row), not Alpine-reactive state: the CSP build evaluates NO
    // expressions in ANY directive, including a comparison, so
    // `x-show="editing === id"` would be silently inert
    // (TestAlpineDirectivesAreCSPSafe catches exactly this). Same shape as
    // columnPicker.apply()'s classList.toggle('col-hidden', ...) -- see
    // .saved-view-rename / .is-editing in app.css.
    closeAnyRename() {
      document.querySelectorAll('.saved-view-row.is-editing').forEach((row) => {
        row.classList.remove('is-editing');
      });
    },

    // The id and current name ride on the clicked button as data-view /
    // data-name -- same pattern as columnPicker.toggleColumn's data-col --
    // because the CSP build cannot pass an argument from an x-on
    // expression. At most one row is ever mid-rename: closeAnyRename first,
    // so switching from renaming one view straight to another does not
    // leave two forms open.
    startRename(event) {
      this.closeAnyRename();
      const row = event.target.closest('.saved-view-row');
      if (!row) {
        return;
      }
      row.classList.add('is-editing');
      const input = row.querySelector('.saved-view-rename input[name="name"]');
      if (input) {
        input.value = event.target.dataset.name || '';
        input.focus();
      }
    },

    cancelRename(event) {
      const row = event.target.closest('.saved-view-row');
      if (row) {
        row.classList.remove('is-editing');
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

// Submit-on-change fields (data-submit-on-change), CSP-safe.
//
// onchange="this.form.requestSubmit()" is an inline handler, and this app's
// own CSP is script-src 'self' with no unsafe-inline (middleware.go's
// Content-Security-Policy) -- the browser drops the attribute silently
// rather than erroring anywhere visible. user_row.html's "sees costs"
// checkbox carried exactly this: it toggled visually and never submitted,
// so an administrator could not grant can_see_costs through the UI at all
// (item 6, 2026-09-02 group-a-1-1 round).
//
// Delegated on document rather than bound to each element: the roster this
// checkbox lives on is swapped by HTMX on every one of the row's own
// mutations (role, costs, active, scrub, project assign/release -- see
// user_row.html's own comment), and a per-element binding would need
// re-attaching after every one of those swaps. A delegated listener needs
// no rebinding because it is never bound to the element in the first place.
//
// Only `change`, not `input`: this is for checkboxes and selects whose value
// is only meaningful once committed, not text fields mid-keystroke.
document.addEventListener('change', (event) => {
  if (event.target.matches && event.target.matches('[data-submit-on-change]') && event.target.form) {
    event.target.form.requestSubmit();
  }
});
