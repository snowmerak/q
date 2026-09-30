import type { DelegationNode, FlatDelegation } from './types';

export function flattenDelegations(nodes: DelegationNode[], depth = 0, parentPath = ''): FlatDelegation[] {
  return nodes.flatMap((node) => {
    const path = parentPath ? `${parentPath}/${node.bookmark.invocation_id}` : node.bookmark.invocation_id;
    return [{ ...node, depth, path }, ...flattenDelegations(node.children || [], depth + 1, path)];
  });
}
