// Inputs update on every keystroke, while their save runs on change/blur.
// Keep those newer edits when a save acknowledgement or poll brings a snapshot.
export function preserveProviderDrafts<T extends { id: string; _original_id?: string }>(
  incoming: T[], current: T[], previous: T[], fields: readonly (keyof T)[]
) {
  for (const provider of incoming) {
    const draft = current.find(item => (item._original_id || item.id) === provider.id)
      ?? current.find(item => item.id === provider.id);
    if (!draft) continue;
    const baseline = previous.find(item => item.id === (draft._original_id || draft.id));
    if (!baseline) continue;
    for (const field of fields) {
      if (draft[field] !== baseline[field]) provider[field] = draft[field];
    }
  }
}
