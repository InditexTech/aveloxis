.. SPDX-FileCopyrightText: 2026 Sean Goggins, University of Missouri, Derek Howard
.. SPDX-License-Identifier: MIT

.. The one reStructuredText page in docs/: the openapi directive emits
   reStructuredText, which a MyST page would read as Markdown.

API reference
=============

Every operation of the Aveloxis REST API, grouped by area. This page is
rendered from :download:`openapi.yaml <openapi.yaml>` when the
documentation is built; the :doc:`conventions <conventions>` explain the
shared rules (access, errors, rate limits, caching) that each entry
assumes. Each entry names its operation (``operationId``), states who may
call it, and ends with the handler that serves it in ``internal/api``.

.. openapi:: openapi.yaml
