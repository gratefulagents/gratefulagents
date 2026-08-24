def execute(data):
    scale = 10 ** data["internal_decimals"]
    return {"minted": data["raw_amount"] * data["price"] * scale}
