// Copyright 2025 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.
/**
 * @fileoverview A library to identify and templatize manually constructed DataGrid.
 */

import type {TSESTree} from '@typescript-eslint/utils';

import {isIdentifier, isIdentifierChain, isMemberExpression} from './ast.ts';
import {DomFragment} from './dom-fragment.ts';
type Identifier = TSESTree.Identifier;
type Node = TSESTree.Node;
type CallExpression = TSESTree.CallExpression;

export const dataGrid = {
  create(context) {
    const sourceCode = context.getSourceCode();
    return {
      methodCall(
          property: Identifier, firstArg: Node, _secondArg: Node|undefined, domFragment: DomFragment,
          _call: CallExpression) {
        if (domFragment.tagName !== 'devtools-data-grid') {
          return false;
        }
        if (isIdentifier(property, 'setRowContextMenuCallback')) {
          domFragment.eventListeners.push({
            key: 'contextmenu',
            value: firstArg,
          });
          return true;
        }
        if (isIdentifier(property, 'setStriped')) {
          domFragment.booleanAttributes.push({
            key: 'striped',
            value: firstArg,
          });
          return true;
        }
        if (isIdentifier(property, 'renderInline')) {
          domFragment.booleanAttributes.push({
            key: 'inline',
            value: 'true',
          });
          return true;
        }
        return false;
      },
      getEvent(event: Node): string |
          null {
            switch (sourceCode.getText(event)) {
              case 'DataGrid.DataGrid.Events.SELECTED_NODE':
              case 'DataGrid.DataGrid.Events.DESELECTED_NODE':
                return 'select';
              case 'DataGrid.DataGrid.Events.SORTING_CHANGED':
                return 'sort';
              default:
                return null;
            }
          },
      NewExpression(node) {
        if (isIdentifierChain(node.callee, ['DataGrid', 'DataGrid', 'DataGridImpl']) ||
            isIdentifierChain(node.callee, ['DataGrid', 'ViewportDataGrid', 'ViewportDataGrid']) ||
            isIdentifierChain(node.callee, ['DataGrid', 'SortableDataGrid', 'SortableDataGrid'])) {
          const domFragment = DomFragment.getOrCreate(node, sourceCode);
          domFragment.tagName = 'devtools-data-grid';
          const params = node.arguments[0];
          if (!params) {
            return;
          }
          if (params.type !== 'ObjectExpression') {
            return;
          }
          for (const property of params.properties) {
            if (property.type !== 'Property') {
              continue;
            }
            if (isIdentifier(property.key, 'displayName')) {
              domFragment.attributes.push({key: 'name', value: property.value});
            } else if (isIdentifier(property.key, 'columns')) {
              let columns = property.value;
              const tableFragment = new DomFragment();
              tableFragment.tagName = 'table';
              domFragment.children.push(tableFragment);
              const columnsFragment = tableFragment.appendChild(columns, sourceCode);
              columnsFragment.tagName = 'tr';
              if (columns.type !== 'ArrayExpression') {
                if (columnsFragment.initializer?.parent?.type === 'VariableDeclarator') {
                  columns = columnsFragment.initializer.parent.init;
                } else if (columnsFragment.initializer?.parent?.type === 'AssignmentExpression') {
                  columns = columnsFragment.initializer.parent.right;
                }
              }
              if (columns.type !== 'ArrayExpression') {
                continue;
              }
              for (const column of columns.elements) {
                if (column.type !== 'ObjectExpression') {
                  continue;
                }
                const columnFragment = columnsFragment.appendChild(column, sourceCode);
                columnFragment.tagName = 'th';
                for (const property of column.properties) {
                  if (property.type !== 'Property' || isIdentifier(property.value, 'undefined')) {
                    continue;
                  }
                  if (isIdentifier(property.key, ['id', 'weight', 'width'])) {
                    columnFragment.attributes.push({key: property.key.name, value: property.value});
                  } else if (isIdentifier(property.key, 'align')) {
                    const value: Node|string|null = isMemberExpression(
                        property.value, n => isIdentifierChain(n, ['DataGrid', 'DataGrid', 'Align']),
                        n => n.type === 'Identifier');
                    columnFragment.attributes.push({
                      key: property.key.name,
                      value: value ? (value as Identifier).name.toLowerCase() : property.value
                    });
                  } else if (isIdentifier(property.key, ['sortable', 'editable'])) {
                    columnFragment.booleanAttributes.push({key: property.key.name, value: property.value});
                  } else if (isIdentifier(property.key, 'fixedWidth')) {
                    columnFragment.booleanAttributes.push({key: 'fixed', value: property.value});
                  } else if (isIdentifier(property.key, 'dataType')) {
                    const value: Node|string|null = isMemberExpression(
                        property.value, n => isIdentifierChain(n, ['DataGrid', 'DataGrid', 'DataType']),
                        n => n.type === 'Identifier');
                    columnFragment.attributes.push({
                      key: property.key.name,
                      value: value ? (value as Identifier).name.toLowerCase() : property.value
                    });
                  } else if (isIdentifier(property.key, ['title', 'titleDOMFragment'])) {
                    columnFragment.textContent = property.value;
                  }
                }
              }
            } else if (isIdentifier(property.key, 'refreshCallback') && !isIdentifier(property.value, 'undefined')) {
              domFragment.eventListeners.push({
                key: 'refresh',
                value: property.value,
              });
            } else if (isIdentifier(property.key, 'deleteCallback') && !isIdentifier(property.value, 'undefined')) {
              domFragment.eventListeners.push({
                key: 'delete',
                value: property.value,
              });
            }
          }
        }
      },
    };
  }
};
