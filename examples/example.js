import http from 'k6/http';
import { group, check, sleep } from 'k6';

const target = __ENV.TARGET || 'http://localhost:8099';

export const options = {
  vus: 3,
  duration: '6s',
  // Breaking-IT k6 fork: attaches request/response detail to failed requests so
  // requests_error rows get headers and body. Without it those columns are NULL.
  httpErrorData: true,
};

export default function () {
  group('browse', function () {
    const home = http.get(`${target}/`, { tags: { name: 'home' } });
    check(home, { 'home is 200': (r) => r.status === 200 });

    group('detail', function () {
      const detail = http.post(`${target}/products/12345/detail?ref=abcdef`,
        JSON.stringify({ productId: '12345' }),
        { headers: { 'Content-Type': 'application/json' }, tags: { name: 'detail' } });
      check(detail, { 'detail is 200': (r) => r.status === 200 });
    });
  });
  sleep(0.2);
}
