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

hdr = fits.Header([('IMAGETYP', 'Light Frame'), ('EXPTIME', 15.0), ('OBJECT', "It's M 31")])
def fz(name, data, **kw):
    fits.HDUList([fits.PrimaryHDU(), fits.CompImageHDU(data, header=hdr, **kw)]).writeto(name, overwrite=True)
fz('rice16.fits.fz', pat, compression_type='RICE_1')                       # uint16: BZERO 32768
fz('rice32.fits.fz', pat.astype(np.int32) - 3000, compression_type='RICE_1')  # negative values too
fz('gzip2.fits.fz', pat.astype(np.int16), compression_type='GZIP_2')
noise = (np.sin(x * 1.3) * np.cos(y * 0.7) * 20).astype(np.float32)
fz('float.fits.fz', pat.astype(np.float32) + noise, compression_type='RICE_1', quantize_level=16, quantize_method=1)

desc = '\n'.join(str(c) for c in hdr.cards)
tifffile.imwrite('gray16.tif', pat, description=desc, metadata=None, compression='zlib')
