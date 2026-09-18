# Settings — people, fields and vocabularies

> Covers: `/users`, `/custom-fields`, `/tags`, `/vocabularies`, `/inflation`
> Regenerated when: roles, cost visibility, user scrubbing or the custom-field
> model changes.

Everything under Settings is **administrative rather than operational**. None of
it describes the estate; all of it decides who may say things about the estate,
and in what vocabulary.

## Users

![The users page. One user, admin, source local, with a role picker offering administrator, observer and project_owner but overridden by a note reading "Administrator (from INV_ADMIN_USERS) — overrides the role picker below. See docs/RECOVERY.md." Columns for sees costs, projects, state, and controls for Deactivate and Scrub. A banner reads "0 active administrators. The last one cannot be demoted, deactivated or scrubbed."](../img/settings-1-users.png)

**This is the only place a role is granted.** If you have read the directory
fragment, this is where the distinction it insists on becomes concrete: signing
in is your directory's decision, and everything on this page is invctl's.

Four separate things live in one row, and they are separate on purpose:

| | |
|---|---|
| **Role** | administrator, project owner, or observer |
| **Sees costs** | a grant of its own, not implied by any role except Administrator |
| **Projects** | what a project owner's write scope actually covers — an owner with none can change nothing |
| **State** | active, deactivated, or scrubbed |

**`INV_ADMIN_USERS` overrides the picker.** A username in that variable is an
Administrator regardless of what the role column says, and the page tells you so
rather than letting you set a role that will not take effect. That is the
break-glass path — it is in configuration rather than in the database precisely
so that a database somebody has locked themselves out of can still be recovered.

**The last administrator cannot be demoted, deactivated or scrubbed.** An estate
with nobody who can grant roles is an estate nobody can fix from inside, so the
software refuses the step that would create one.

### Deactivate and scrub are different, and the difference is the point

**Deactivating** stops somebody signing in. Their account stays, their name
still resolves in the change log, and reactivating is one click.

**Scrubbing** answers a GDPR erasure request. It removes the personal data and
leaves the audit trail intact — which is possible only because `change_log`
never stored a name in the first place: it holds an opaque account id, and the
UI resolves it for display. After a scrub the log keeps its integrity and simply
stops resolving that id to a person.

Their saved views go too. A saved view is one person's shortcut, so once its
owner is erased it belongs to nobody and serves nothing; keeping it would be
retaining personal data with no purpose.

Every change on this page is a permanent record in the change log. Granting
somebody write access is exactly the kind of decision that should be
reconstructable a year later.

## Custom fields

![The custom fields page. Asset fields include criticality_tier (select, owned by a team recorded as "decommissioned (retired)"), is_leased (boolean, platform@example.com), last_physical_audit (date), and power_budget_watts (number, unassigned). Each row names who defined it, when, how many values exist, and carries Edit, Options and Retire controls.](../img/settings-2-custom-fields.png)

Estate-specific attributes an administrator added **without a migration**.

The page answers one question that otherwise costs somebody a phone call:
*"did invctl ship this field, or did someone here add it?"* Every field names
who defined it, when, and why. A field with no owning team — or one whose owner
has since disbanded, as `criticality_tier` shows — turns up on the ownership
report rather than quietly persisting as folklore.

**The value count is the useful column.** A field with 27 values is load-bearing;
one with none is a definition somebody meant to use. Retiring the second costs
nothing, and retiring the first needs a conversation.

**Custom field values are deliberately not written into the change log.** The
log records that values changed and how many, never their text. A free-text
field is the one place an operator can type anything — including personal data
— and an append-only log that captured it would be a retention problem nobody
chose. The current value lives on the entity's own page, where it can be
corrected or removed.

## Vocabularies, tags and inflation

Three smaller pages, all the same shape: **things the estate names for itself**
rather than things invctl decided.

- **Vocabularies** are the lookup tables behind the enumerated columns — asset
  kinds, environment roles, address roles, cost kinds. Adding a term is a
  declaration that this estate has such a thing; every term carries a
  description, because a lookup value whose meaning lives in one person's head
  is a value that gets used two different ways.
- **Tags** are free grouping applied across entity types, and every list page
  filters by them. They are a filter rather than a hierarchy — an asset can
  carry several, and nothing infers one from another.
- **Inflation** records a yearly rate in basis points, used by the supplier
  report to separate *"this supplier raised the price"* from *"money is worth
  less"*. Without it, every multi-year contract looks like a price rise. It is
  recorded per year and never inferred from the data.
