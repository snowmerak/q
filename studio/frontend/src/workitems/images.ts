import { requestResponse } from '../api';

export const imageTypes = 'image/png,image/jpeg,image/webp,image/gif';
const types = new Set(imageTypes.split(','));

export function appendImages(current: File[], next: File[]): File[] {
  const files = [...current, ...next];
  if (files.length > 4) throw new Error('Attach up to four images.');
  if (files.some(file => !types.has(file.type))) throw new Error('Choose PNG, JPEG, WebP, or GIF images.');
  if (files.some(file => file.size > 4 * 1024 * 1024) || files.reduce((total, file) => total + file.size, 0) > 8 * 1024 * 1024) {
    throw new Error('Images can be up to 4 MiB each and 8 MiB combined.');
  }
  return files;
}

export function imageDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error('Could not read the image.'));
    reader.readAsDataURL(file);
  });
}

export function imageFiles(event: ClipboardEvent): File[] {
  return Array.from(event.clipboardData?.files || []).filter(file => file.type.startsWith('image/'));
}

export async function uploadImages(workspace: string, files: File[]): Promise<string[]> {
  return Promise.all(files.map(async file => {
    const url = await imageDataURL(file);
    const response = await requestResponse(`/api/v1/projects/${encodeURIComponent(workspace)}/assets`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ data: url.split(',')[1] })
    });
    return 'q-asset:' + (await response.json() as { id: string }).id;
  }));
}

export function assetURL(workspace: string, id: string): string {
  return `/api/v1/projects/${encodeURIComponent(workspace)}/assets/${encodeURIComponent(id)}`;
}

export function imageRefs(refs?: string[] | null): string[] {
  return (refs || []).filter(ref => /^q-asset:[a-f0-9]{64}$/.test(ref)).map(ref => ref.slice('q-asset:'.length));
}
