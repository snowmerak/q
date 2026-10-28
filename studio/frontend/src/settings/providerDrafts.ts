const edits = new WeakMap<object, Map<PropertyKey, number>>();
let editVersion = 0;

export function markProviderDraft<T extends object>(provider: T, field: keyof T) {
  let fields = edits.get(provider);
  if (!fields) {
    fields = new Map();
    edits.set(provider, fields);
  }
  fields.set(field, ++editVersion);
}

export function captureProviderDraft(provider: object) {
  return new Map(edits.get(provider));
}

export function acknowledgeProviderDraft(provider: object, submitted: Map<PropertyKey, number>) {
  const fields = edits.get(provider);
  for (const [field, version] of submitted) {
    if (fields?.get(field) === version) fields.delete(field);
  }
}

// Inputs update on every keystroke, while their save runs on change/blur.
// Versions also preserve edits back to the previous saved value during a request.
export function preserveProviderDrafts<T extends { id: string; _original_id?: string }>(
  incoming: T[], current: T[], previous: T[], fields: readonly (keyof T)[]
) {
  for (const provider of incoming) {
    const draft = current.find(item => (item._original_id || item.id) === provider.id)
      ?? current.find(item => item.id === provider.id);
    if (!draft) continue;
    const fieldsEdited = edits.get(draft);
    // Queued acknowledgements and refreshed objects share the same edit versions.
    if (fieldsEdited) edits.set(provider, fieldsEdited);
    const baseline = previous.find(item => item.id === (draft._original_id || draft.id));
    for (const field of fields) {
      const changed = fieldsEdited ? fieldsEdited.has(field) : baseline && draft[field] !== baseline[field];
      if (changed) provider[field] = draft[field];
    }
  }
}
