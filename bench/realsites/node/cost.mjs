import fs from 'node:fs';
import path from 'node:path';
import { HERE } from './lib.mjs';

export function loadPrices() { return JSON.parse(fs.readFileSync(path.join(HERE, '..', 'prices.json'), 'utf8')); }

// usage: {input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens} (per request)
export function costUSD(prices, model, u) {
  const m = prices.models[model];
  if (!m) return null; // unknown model: report tokens, not a made-up cost
  const prompt = (u.input_tokens || 0) + (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0);
  const long = m.long_threshold && prompt > m.long_threshold;
  const pin = long ? m.in_long : m.in, pout = long ? m.out_long : m.out;
  return ((u.input_tokens || 0) * pin + (u.cache_read_input_tokens || 0) * pin * prices.cache_read_mult +
    (u.cache_creation_input_tokens || 0) * pin * prices.cache_write_mult + (u.output_tokens || 0) * pout) / 1e6;
}
