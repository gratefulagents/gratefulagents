def execute(data):
    minted = data["raw_amount"] * data["price"] * 10 ** data["internal_decimals"]
    return {"minted": minted}
