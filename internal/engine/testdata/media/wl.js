// AudioWorklet processor used by media_audio_test.go: out = in * gain-param (+ port message count).
class GainProc extends AudioWorkletProcessor {
  static get parameterDescriptors() { return [{ name: 'g', defaultValue: 2, minValue: 0, maxValue: 10, automationRate: 'a-rate' }]; }
  constructor(options) { super(options); this.n = 0; this.port.onmessage = (e) => { this.last = e.data; }; }
  process(inputs, outputs, parameters) {
    const inp = inputs[0], out = outputs[0], g = parameters.g;
    for (let c = 0; c < out.length; c++) {
      const src = inp[c] || inp[0];
      for (let i = 0; i < out[c].length; i++) out[c][i] = (src ? src[i] : 0) * (g.length > 1 ? g[i] : g[0]);
    }
    this.n++;
    if (this.n === 1) this.port.postMessage({ rate: sampleRate, frame: currentFrame });
    return true;
  }
}
registerProcessor('gain-proc', GainProc);
