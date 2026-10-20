# Writes the format fixtures with independent writers: sergio-dr/xisf,
# astropy (cfitsio's tile compression) and tifffile.
#   python3 -m venv v && v/bin/pip install xisf astropy tifffile
#   v/bin/python -I gen.py   (in this directory)
# Pixels follow pattern() in formats_test.go: (37x + 101y) mod 4000 + 1000,
# row y = 0 stored first. Image size 40 x 30.
import numpy as np
from astropy.io import fits
from xisf import XISF
import tifffile

W, H = 40, 30
y, x = np.mgrid[0:H, 0:W]
pat = ((37 * x + 101 * y) % 4000 + 1000).astype(np.uint16)
kw = {'IMAGETYP': [{'value': "'Light Frame'", 'comment': ''}],
      'EXPTIME': [{'value': '15.', 'comment': ''}],
      'OBJECT': [{'value': "'It''s M 31'", 'comment': ''}],
      'BAYERPAT': [{'value': "'RGGB'", 'comment': ''}]}
for codec, sh in [(None, False), ('zlib', False), ('lz4hc', True), ('zstd', True)]:
    XISF.write('xisf_%s.xisf' % codec, pat[:, :, None], 'gen.py', {'FITSKeywords': kw}, {}, codec=codec, shuffle=sh)
rgb = np.stack([pat, pat + 300, pat + 600], axis=2).astype(np.float32) / 65535
XISF.write('xisf_rgb.xisf', rgb, 'gen.py', {'FITSKeywords': kw}, {}, codec='zstd', shuffle=True)
# The other sample formats: UInt8 holds the pattern mod 256.
XISF.write('xisf_uint8.xisf', (pat % 256).astype(np.uint8)[:, :, None], 'gen.py', {'FITSKeywords': kw}, {}, codec='zlib')
XISF.write('xisf_uint32.xisf', (pat.astype(np.uint32) + 100000)[:, :, None], 'gen.py', {'FITSKeywords': kw}, {}, codec='zlib', shuffle=True)
XISF.write('xisf_float64.xisf', pat.astype(np.float64)[:, :, None], 'gen.py', {'FITSKeywords': kw}, {}, codec='lz4', shuffle=True)

hdr = fits.Header([('IMAGETYP', 'Light Frame'), ('EXPTIME', 15.0), ('OBJECT', "It's M 31")])
def fz(name, data, **kw):
    fits.HDUList([fits.PrimaryHDU(), fits.CompImageHDU(data, header=hdr, **kw)]).writeto(name, overwrite=True)
fz('rice16.fits.fz', pat, compression_type='RICE_1')                       # uint16: BZERO 32768
fz('rice32.fits.fz', pat.astype(np.int32) - 3000, compression_type='RICE_1')  # negative values too
fz('gzip2.fits.fz', pat.astype(np.int16), compression_type='GZIP_2')
fz('gzip1.fits.fz', pat.astype(np.int16), compression_type='GZIP_1')
fz('nocompress.fits.fz', pat.astype(np.int32), compression_type='NOCOMPRESS')
fz('rice8.fits.fz', (pat % 256).astype(np.uint8), compression_type='RICE_1')
fz('gzip1f64.fits.fz', pat.astype(np.float64) + 0.25, compression_type='GZIP_1', quantize_level=0.0)  # lossless
noise = (np.sin(x * 1.3) * np.cos(y * 0.7) * 20).astype(np.float32)
# HCOMPRESS_1 and PLIO_1, each with astropy's own decode (cfitsio) as the
# expected pixels in a plain FITS file beside it, *_ref.fits. Odd 37 x 23
# tiles; seeded noise so bit planes are written directly, not quadtree-coded.
rng = np.random.default_rng(7)
odd = (rng.normal(2000, 300, (23, 37)) + 40 * np.sin(np.mgrid[0:23, 0:37][1] / 3)).astype(np.int32)
def fzref(name, data, **kw):
    fz(name + '.fits.fz', data, **kw)
    fits.PrimaryHDU(fits.getdata(name + '.fits.fz')).writeto(name + '_ref.fits', overwrite=True)
fzref('hcomp_noise', odd.astype(np.int16), compression_type='HCOMPRESS_1', hcomp_scale=0, tile_shape=(6, 37))  # 4 tiles, the last 5 rows
fzref('hcomp_lossy', odd, compression_type='HCOMPRESS_1', hcomp_scale=64, hcomp_smooth=True, tile_shape=(23, 37))
# A smooth field: SMOOTH's monotonicity tests pass, so it moves coefficients both ways.
yo, xo = np.mgrid[0:23, 0:37]
fzref('hcomp_smooth', (500 + 37 * xo - 23 * yo + 0.4 * xo * yo).astype(np.int32), compression_type='HCOMPRESS_1', hcomp_scale=16, hcomp_smooth=True, tile_shape=(23, 37))
# Runs of zeros and of values beyond 12 bits, rising and falling, for every opcode.
runs = np.where((x // 4 + y) % 3 == 0, 0, (x * 104729 + y * 7919) % (1 << 23)).astype(np.int32)
runs[::2, 5:9] = 4000 + y[::2, 5:9]
runs[:, 10:30] = 1000 + 5 * np.abs(x[:, 10:30] % 10 - 5)  # single pixels stepping up and down
runs[:, 30:40] = 2000 + 3 * (x[:, 30:40] // 2)  # pairs, each a small rise
fzref('plio', runs, compression_type='PLIO_1')
fz('float.fits.fz', pat.astype(np.float32) + noise, compression_type='RICE_1', quantize_level=16, quantize_method=1)

desc = '\n'.join(str(c) for c in hdr.cards)
tifffile.imwrite('gray16.tif', pat, description=desc, metadata=None, compression='zlib')
tifffile.imwrite('gray8.tif', (pat % 256).astype(np.uint8), description=desc, metadata=None)
tifffile.imwrite('rgb16.tif', np.stack([pat, pat + 300, pat + 600], axis=2), description=desc, metadata=None, photometric='rgb')
p8 = (pat % 256).astype(np.uint8)
tifffile.imwrite('rgb8.tif', np.stack([p8, p8, p8], axis=2), description=desc, metadata=None, photometric='rgb')
