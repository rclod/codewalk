# checkout

A fictional checkout service used as a codewalk benchmark fixture.

`POST /orders` records a pending order and returns immediately. A separate
worker process (`cmd/worker`) authorises payment and moves the order to its
final state.
